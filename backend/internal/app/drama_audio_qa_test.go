package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func TestDramaAudioASRAndLayeredQASandbox(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	s, fake, plan := dramaAudioFixture(t)
	s.dataDir = t.TempDir()
	root := filepath.Join(s.dataDir, "resources")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(root, "sandbox.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=180x320:rate=5", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000", "-t", "6", "-c:v", "mpeg4", "-c:a", "aac", "-y", video).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v %s", err, out)
	}
	wav := filepath.Join(root, "sandbox.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-i", video, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-y", wav).CombinedOutput(); err != nil {
		t.Fatalf("audio %v %s", err, out)
	}
	before, err := s.DramaSegmentDetail("usr-drama", plan.DramaID, plan.EpisodeID, 0)
	if err != nil {
		t.Fatal(err)
	}
	videoStat, _ := os.Stat(video)
	if err := fake.db.Model(&model.Resource{}).Where("id = ?", before.VideoVersions[0].ResourceID).Updates(map[string]any{"provider": "local", "object_key": "sandbox.mp4", "size": videoStat.Size(), "duration_ms": 6000}).Error; err != nil {
		t.Fatal(err)
	}
	audioID := fake.resource("usr-drama", "audio", "audio/wav")
	wavStat, _ := os.Stat(wav)
	if err := fake.db.Model(&model.Resource{}).Where("id = ?", audioID).Updates(map[string]any{"provider": "local", "object_key": "sandbox.wav", "size": wavStat.Size(), "duration_ms": 6000}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDramaSegmentAudio("usr-drama", plan); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MutateDramaAudioCue("usr-drama", "bind", MutateDramaAudioCueRequest{DramaAudioScope: plan.DramaAudioScope, ExpectedAudioRevision: 1, ExpectedShotRevision: plan.ExpectedShotRevision, CueID: plan.Tracks[0].ID, ResourceID: audioID, IdempotencyKey: "bind-sandbox"}); err != nil {
		t.Fatal(err)
	}
	inspect := InspectDramaAudioRequest{DramaAudioScope: plan.DramaAudioScope, AudioRevision: 2, CueID: plan.Tracks[0].ID, Source: "cue", IdempotencyKey: "asr-unconfigured"}
	t.Setenv(whisperLangEnv, "")
	unknown, err := s.InspectDramaAudio(context.Background(), "usr-drama", inspect)
	if err != nil || unknown["status"] != "unknown" {
		t.Fatalf("unconfigured ASR %+v %v", unknown, err)
	}
	unknownID := unknown["id"].(string)
	read, err := s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 0)
	if err != nil || read["qa"].(map[string]any)["accepted"] != false {
		t.Fatalf("AAC implies acceptance %+v %v", read, err)
	}
	requests := 0
	asr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/inference" {
			t.Error("wrong ASR route")
		}
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("prompt") != "" {
			t.Error("expected script leaked into recognizer prompt")
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer file.Close()
		header := make([]byte, 12)
		if _, err := io.ReadFull(file, header); err != nil || string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
			t.Error("ASR did not receive actual decoded wav")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"language": "zh", "segments": []any{map[string]any{"start": 0.0, "end": 1.0, "text": plan.Tracks[0].Text}}})
	}))
	defer asr.Close()
	t.Setenv(whisperLangEnv, asr.URL)
	inspect.IdempotencyKey, inspect.ExpectedCheckID = "asr-cue", unknownID
	matched, err := s.InspectDramaAudio(context.Background(), "usr-drama", inspect)
	if err != nil || matched["status"] != "matched" {
		t.Fatalf("ASR %+v %v", matched, err)
	}
	proof := matched["result"].(map[string]any)
	if proof["alignment"].(map[string]any)["wordAlignmentStatus"] != "unavailable" || len(matched["resourceSha256"].(string)) != 64 {
		t.Fatalf("invented evidence %+v", matched)
	}
	inspect.Source, inspect.IdempotencyKey, inspect.ExpectedCheckID = "video", "asr-video", ""
	videoCheck, err := s.InspectDramaAudio(context.Background(), "usr-drama", inspect)
	if err != nil || videoCheck["status"] != "matched" {
		t.Fatalf("video ASR %+v %v", videoCheck, err)
	}
	qa := DramaAudioQARequest{DramaAudioScope: plan.DramaAudioScope, AudioRevision: 2, Layer: "final", Status: "approved", Reason: "模拟验收", ObservedEndMs: plan.Tracks[0].EndMs, IdempotencyKey: "premature-final"}
	if _, err := s.RecordDramaAudioQA("usr-drama", qa); err == nil {
		t.Fatal("ASR alone approved final")
	}
	qa.Layer, qa.CueID, qa.Status, qa.MouthState, qa.IdempotencyKey, qa.Reason = "mouth", plan.Tracks[0].ID, "rejected", "speaking", "reject-mouth", "内心独白时嘴部开合，拒绝"
	rejected, err := s.RecordDramaAudioQA("usr-drama", qa)
	if err != nil {
		t.Fatal(err)
	}
	read, err = s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 0)
	if err != nil || read["qaStatus"] != "rejected" {
		t.Fatalf("lost rejection %+v %v", read, err)
	}
	qa.Status, qa.MouthState, qa.IdempotencyKey = "approved", "not_visible", "bad-mouth-approval"
	qa.ExpectedCheckID = rejected["id"].(string)
	if _, err := s.RecordDramaAudioQA("usr-drama", qa); err == nil {
		t.Fatal("invisible mouth approved closed")
	}
	qa.MouthState, qa.IdempotencyKey, qa.Reason = "closed", "review-mouth", "模拟人工逐帧确认闭口"
	if _, err := s.RecordDramaAudioQA("usr-drama", qa); err != nil {
		t.Fatal(err)
	}
	for _, layer := range []string{"speaker", "content", "picture"} {
		qa.Layer, qa.ExpectedCheckID, qa.IdempotencyKey = layer, "", "review-"+layer
		qa.CueID = ""
		if layer == "speaker" {
			qa.CueID = plan.Tracks[0].ID
			qa.ObservedSpeaker = plan.Tracks[0].Speaker
		}
		if _, err := s.RecordDramaAudioQA("usr-drama", qa); err != nil {
			t.Fatalf("layer %s %v", layer, err)
		}
	}
	qa.Layer, qa.CueID, qa.IdempotencyKey = "final", "", "review-final"
	if _, err := s.RecordDramaAudioQA("usr-drama", qa); err != nil {
		t.Fatal(err)
	}
	read, err = s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 0)
	if err != nil || read["qa"].(map[string]any)["accepted"] != true {
		t.Fatalf("layered QA %+v %v", read, err)
	}
	inspect.ExpectedCheckID, inspect.IdempotencyKey = videoCheck["id"].(string), "reinspect-video"
	if _, err := s.InspectDramaAudio(context.Background(), "usr-drama", inspect); err != nil {
		t.Fatal(err)
	}
	read, err = s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 0)
	if err != nil || read["qa"].(map[string]any)["accepted"] != false || read["qa"].(map[string]any)["finalStatus"] != "stale" {
		t.Fatal("new evidence reused old final acceptance")
	}
	// A new audio version never inherits acceptance, while historical rejection evidence survives.
	plan.ExpectedAudioRevision, plan.IdempotencyKey = 2, "new-version"
	if _, err := s.SaveDramaSegmentAudio("usr-drama", plan); err != nil {
		t.Fatal(err)
	}
	read, err = s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 0)
	if err != nil || read["qaStatus"] != "unreviewed" {
		t.Fatalf("new version inherited approval %+v %v", read, err)
	}
	old, err := s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 2)
	bytes, _ := json.Marshal(old)
	if err != nil || !strings.Contains(string(bytes), "内心独白时嘴部开合") {
		t.Fatal("historical rejection erased")
	}
	after, err := s.DramaSegmentDetail("usr-drama", plan.DramaID, plan.EpisodeID, 0)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("QA changed formal production")
	}
	if requests != 3 {
		t.Fatalf("unexpected ASR calls %d", requests)
	}
}
