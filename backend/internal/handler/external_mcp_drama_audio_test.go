package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	"yingce/backend/internal/service"
)

type audioSandboxAuthTransport struct{ token string }

func (t audioSandboxAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}

// Real HTTP authentication, MCP SDK, database, ffprobe/ffmpeg and persistent QA; isolated from formal data.
func TestExternalMCPDramaAudioSandbox(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	t.Setenv("CANVAS_WHISPER_BASE_URL", "")
	root := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "sandbox.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(database.Models()...); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := os.MkdirAll(filepath.Join(root, "resources"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "resources", "sandbox.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=180x320:rate=5", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000", "-t", "3", "-c:v", "mpeg4", "-c:a", "aac", "-y", file).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v %s", err, out)
	}
	stat, _ := os.Stat(file)
	for _, row := range []any{
		&model.User{ID: "sandbox-user", Username: "sandbox", Status: model.UserStatusActive},
		&model.User{ID: "sandbox-other", Username: "other", Status: model.UserStatusActive},
		&model.Project{ID: "sandbox-drama", UserID: "sandbox-user", Type: "short-drama", Status: model.ProjectStatusActive},
		&model.DramaProduction{ProjectID: "sandbox-drama", UserID: "sandbox-user", SettingsJSON: "{}"},
		&model.DramaEpisode{ID: "sandbox-episode", ProjectID: "sandbox-drama"},
		&model.DramaShot{ID: "sandbox-shot-23", ProjectID: "sandbox-drama", EpisodeID: "sandbox-episode", Position: 23, SegmentIndex: 5, DialogueMode: "inner_monologue", Speaker: "陈启", Dialogue: "前世周牧拒了收购，", DurationMs: 1000},
		&model.DramaShot{ID: "sandbox-shot-24", ProjectID: "sandbox-drama", EpisodeID: "sandbox-episode", Position: 24, SegmentIndex: 5, DialogueMode: "inner_monologue", Speaker: "陈启", Dialogue: "资金断了，", DurationMs: 1000},
		&model.DramaShot{ID: "sandbox-shot-25", ProjectID: "sandbox-drama", EpisodeID: "sandbox-episode", Position: 25, SegmentIndex: 5, DialogueMode: "inner_monologue", Speaker: "陈启", Dialogue: "最后我们也散了。", DurationMs: 1000},
		&model.DramaSegment{ID: "sandbox-segment", ProjectID: "sandbox-drama", EpisodeID: "sandbox-episode", SegmentIndex: 5, Revision: 7, VideoResourceID: "sandbox-video"},
		&model.DramaTake{ID: "sandbox-take", ProjectID: "sandbox-drama", OwnerID: "sandbox-segment", Kind: "video", ResourceID: "sandbox-video"},
		&model.Resource{ID: "sandbox-video", UserID: "sandbox-user", Kind: "video", Status: model.ResourceStatusReady, Provider: "local", ObjectKey: "sandbox.mp4", MimeType: "video/mp4", Size: stat.Size(), DurationMs: 3000},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := service.New(repository.New(db), root)
	ConfigureRuntime(svc)
	defer ConfigureRuntime(nil)
	key, err := svc.CreateUserAPIKey("sandbox-user", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	RegisterExternalMCPRoutes(router.Group("/api"), svc)
	httpServer := httptest.NewServer(router)
	defer httpServer.Close()
	t.Setenv("CANVAS_MCP_PUBLIC_ORIGIN", httpServer.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connect := func(token string) *mcp.ClientSession {
		client := mcp.NewClient(&mcp.Implementation{Name: "audio-sandbox", Version: "1"}, nil)
		cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/api/mcp", HTTPClient: &http.Client{Transport: audioSandboxAuthTransport{token: token}}, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		return cs
	}
	cs := connect(key.Token)
	catalog, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, tool := range catalog.Tools {
		registered[tool.Name] = true
	}
	if !registered["inspect_segment_video_speech"] || !registered["render_segment_audio"] {
		t.Fatal("new audio tools missing from MCP discovery")
	}
	frameArgs := map[string]any{"dramaId": "sandbox-drama", "episodeId": "sandbox-episode", "segmentIndex": 5, "atSeconds": 0.5}
	var resourcesBefore int64
	db.Model(&model.Resource{}).Count(&resourcesBefore)
	preview, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "preview_segment_frame", Arguments: frameArgs})
	if err != nil || preview.IsError {
		t.Fatalf("frame MCP %v", err)
	}
	images := 0
	for _, block := range preview.Content {
		if img, ok := block.(*mcp.ImageContent); ok && img.MIMEType == "image/jpeg" && len(img.Data) > 100 {
			images++
		}
	}
	if images != 1 {
		t.Fatal("MCP response dropped actual frame image")
	}
	var resourcesAfter int64
	db.Model(&model.Resource{}).Count(&resourcesAfter)
	if resourcesAfter != resourcesBefore {
		t.Fatal("default preview created a resource")
	}
	frameArgs["persistPreview"] = true
	var frameResourceID string
	for i := 0; i < 2; i++ {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "preview_segment_frame", Arguments: frameArgs})
		if err != nil || result.IsError {
			t.Fatalf("persist frame MCP %v", err)
		}
		data, _ := json.Marshal(result.StructuredContent)
		var metadata struct {
			FrameResourceID, ResourceID, FrameMimeType string
			FrameWidth, FrameHeight                    int
		}
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.ResourceID != "sandbox-video" || metadata.FrameResourceID == "" || metadata.FrameWidth != 180 || metadata.FrameHeight != 320 || metadata.FrameMimeType != "image/jpeg" {
			t.Fatalf("incomplete frame resource metadata %+v", metadata)
		}
		if i == 0 {
			frameResourceID = metadata.FrameResourceID
		} else if frameResourceID != metadata.FrameResourceID {
			t.Fatal("MCP preview retry duplicated resource")
		}
	}
	db.Model(&model.Resource{}).Count(&resourcesAfter)
	if resourcesAfter != resourcesBefore+1 {
		t.Fatal("MCP preview did not reuse saved JPEG")
	}
	call := func(name string, args map[string]any, wantError bool) map[string]any {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || r.IsError != wantError {
			t.Fatalf("%s %+v %v", name, r, err)
		}
		var value map[string]any
		if r.StructuredContent != nil {
			b, _ := json.Marshal(r.StructuredContent)
			if err := json.Unmarshal(b, &value); err != nil {
				t.Fatal(err)
			}
		} else if len(r.Content) > 0 {
			if err := json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &value); err != nil {
				t.Fatal(err)
			}
		}
		return value
	}
	scope := map[string]any{"dramaId": "sandbox-drama", "episodeId": "sandbox-episode", "segmentIndex": 5, "revision": 0}
	read := call("get_segment_audio", scope, false)
	shotRevision := read["shotRevision"]
	tracks := []map[string]any{}
	for i, text := range []string{"前世周牧拒了收购，", "资金断了，", "最后我们也散了。"} {
		id := []string{"23", "24", "25"}[i]
		tracks = append(tracks, map[string]any{"id": "inner-" + id, "shotId": "sandbox-shot-" + id, "kind": "speech", "dialogueMode": "inner_monologue", "speaker": "陈启", "text": text, "startMs": i * 1000, "endMs": (i + 1) * 1000, "sourceOffsetMs": 0, "model": map[string]any{}, "voice": "", "instructions": ""})
	}
	save := map[string]any{"dramaId": "sandbox-drama", "episodeId": "sandbox-episode", "segmentIndex": 5, "expectedAudioRevision": 0, "expectedShotRevision": shotRevision, "videoTakeId": "sandbox-take", "tracks": tracks, "idempotencyKey": "sandbox-save"}
	saved := call("save_segment_audio", save, false)
	if saved["revision"] != float64(1) {
		t.Fatalf("save %+v", saved)
	}
	if replay := call("save_segment_audio", save, false); replay["id"] != saved["id"] {
		t.Fatal("replay changed plan")
	}
	inspect := map[string]any{"dramaId": "sandbox-drama", "episodeId": "sandbox-episode", "segmentIndex": 5, "audioRevision": 1, "cueId": "inner-23", "source": "video", "expectedCheckId": "", "idempotencyKey": "sandbox-inspect"}
	measured := call("inspect_segment_audio", inspect, false)
	if measured["status"] != "unknown" || measured["result"].(map[string]any)["hasAudioTrack"] != true {
		t.Fatalf("AAC incorrectly accepted %+v", measured)
	}
	qa := map[string]any{"dramaId": "sandbox-drama", "episodeId": "sandbox-episode", "segmentIndex": 5, "audioRevision": 1, "cueId": "inner-23", "layer": "mouth", "status": "rejected", "reason": "沙箱：独白嘴部开合，不能按通过交付", "observedStartMs": 0, "observedEndMs": 1000, "mouthState": "speaking", "lipSyncChecked": false, "expectedCheckId": "", "idempotencyKey": "sandbox-reject"}
	call("record_segment_audio_qa", qa, false)
	read = call("get_segment_audio", scope, false)
	if read["qaStatus"] != "rejected" {
		t.Fatalf("lost rejection %+v", read)
	}
	if err := db.Model(&model.DramaTake{}).Where("id = ?", "sandbox-take").Update("shot_revision", shotRevision).Error; err != nil {
		t.Fatal(err)
	}
	nativeArgs := map[string]any{"dramaId": "sandbox-drama", "episodeId": "sandbox-episode", "segmentIndex": 5, "videoTakeId": "sandbox-take", "expectedShotRevision": shotRevision, "expectedAudioRevision": 1, "idempotencyKey": "native-window-inspect"}
	native := call("inspect_segment_video_speech", nativeArgs, false)
	if native["accepted"] != false || len(native["shotResults"].([]any)) != 3 || native["version"].(map[string]any)["revision"] != float64(2) {
		t.Fatalf("native inspection %+v", native)
	}
	call("render_segment_audio", map[string]any{"dramaId": "sandbox-drama", "episodeId": "sandbox-episode", "segmentIndex": 5, "expectedShotRevision": shotRevision, "expectedAudioRevision": 2, "nativeAudioMode": "discard", "idempotencyKey": "cannot-render-native-cues"}, true)
	otherKey, err := svc.CreateUserAPIKey("sandbox-other", "sandbox-other")
	if err != nil {
		t.Fatal(err)
	}
	other := connect(otherKey.Token)
	denied, err := other.CallTool(ctx, &mcp.CallToolParams{Name: "get_segment_audio", Arguments: scope})
	if err != nil || !denied.IsError {
		t.Fatal("cross-account audio access")
	}
	var segment model.DramaSegment
	if err := db.First(&segment, "id = ?", "sandbox-segment").Error; err != nil || segment.VideoResourceID != "sandbox-video" || segment.Revision != 7 {
		t.Fatal("MCP QA replaced video")
	}
}
