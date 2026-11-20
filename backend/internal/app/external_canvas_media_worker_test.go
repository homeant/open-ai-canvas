package app

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

func TestExternalCanvasH3WorkerSandbox(t *testing.T) {
	requireFFmpegTools(t)
	s, db := externalCanvasFixture(t)
	s = newService(s.repo, t.TempDir())
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	t.Setenv("CANVAS_PUBLIC_BASE_URL", "https://media.sandbox.example")
	file := filepath.Join(t.TempDir(), "result.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=180x320:rate=5", "-t", "4", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", file).CombinedOutput(); err != nil {
		t.Fatalf("video %v %s", err, out)
	}
	video, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	creates, polls, downloads := 0, 0, 0
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/minimax/v2/video_generation":
			creates++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload["duration"] != float64(4) || payload["ratio"] != "9:16" || payload["resolution"] != "768P" || payload["model"] != "MiniMax-H3" {
				t.Errorf("H3 request lost configuration: %+v", payload)
			}
			content, _ := payload["content"].([]any)
			if len(content) != 2 {
				t.Errorf("H3 missing image input: %+v", content)
			}
			_, _ = w.Write([]byte(`{"task_id":"sandbox-h3","task":{"status":"pending"}}`))
		case "/api/minimax/v2/query/video_generation/sandbox-h3":
			polls++
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "sandbox-h3", "task": map[string]any{"status": "completed", "content": map[string]any{"url": upstream.URL + "/result.mp4"}}})
		case "/result.mp4":
			downloads++
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(video)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	if err := db.Model(&model.ModelChannel{}).Where("id = ?", "channel").Updates(map[string]any{"base_url": upstream.URL, "api_key": "sandbox-placeholder"}).Error; err != nil {
		t.Fatal(err)
	}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 32, 48))); err != nil {
		t.Fatal(err)
	}
	resourceID := externalUploadTestFile(t, s, "h3-reference.png", "image", imageData.Bytes())
	added, err := s.AddExternalCanvasMediaNode("user", ExternalCanvasMediaNodeRequest{ExternalCanvasMutation: ExternalCanvasMutation{CanvasID: "agent-canvas", SnapshotHash: externalCanvasSnapshot(t, s), IdempotencyKey: "worker-review"}, ResourceID: resourceID, Title: "沙箱关键参考图"})
	if err != nil {
		t.Fatal(err)
	}
	id := externalVideoDraft(t, s, []string{added["nodeId"].(string)})
	snapshot := externalCanvasSnapshot(t, s)
	preview, err := s.PreviewExternalCanvasGeneration("user", ExternalCanvasGenerationRequest{CanvasID: "agent-canvas", NodeID: id, SnapshotHash: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	in := ExternalCanvasSubmitRequest{ExternalCanvasMutation: ExternalCanvasMutation{CanvasID: "agent-canvas", SnapshotHash: snapshot, IdempotencyKey: "worker-submit"}, NodeID: id, ExpectedPreviewHash: preview["previewHash"].(string), MaxCostMicrocredits: 40}
	submitted, err := s.SubmitExternalCanvasGeneration("user", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitExternalCanvasGeneration("user", in); err != nil {
		t.Fatal(err)
	}
	task, err := s.repo.ClaimNextTask("canvas-h3-sandbox", 45*time.Second)
	if err != nil || task == nil {
		t.Fatalf("queue %v", err)
	}
	if err := newTaskWorkerCoordinator(s).processClaimedTask(task, nil); err != nil {
		t.Fatal(err)
	}
	finished, err := s.repo.Task(submitted["taskId"].(string))
	if err != nil || finished.Status != model.TaskStatusSucceeded {
		t.Fatalf("H3 worker status=%v err=%v", finished, err)
	}
	read, err := s.GetExternalCanvasGeneration("user", "agent-canvas", id)
	if err != nil {
		t.Fatal(err)
	}
	resource := read["resource"].(map[string]any)
	if resource["durationMs"] != int64(4000) || resource["width"] != 180 || resource["height"] != 320 || read["qaStatus"] != "unreviewed" {
		t.Fatalf("output %+v", read)
	}
	if _, err = s.SyncExternalCanvasGeneration("user", ExternalCanvasSyncRequest{ExternalCanvasMutation: ExternalCanvasMutation{CanvasID: "agent-canvas", SnapshotHash: externalCanvasSnapshot(t, s), IdempotencyKey: "worker-sync"}, NodeID: id, TaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || polls != 1 || downloads != 1 {
		t.Fatalf("create=%d poll=%d download=%d", creates, polls, downloads)
	}
}
