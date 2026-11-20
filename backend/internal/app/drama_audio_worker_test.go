package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

// Real admission, queue worker, output storage and binding; only the upstream TTS is simulated.
func TestDramaAudioTTSWorkerSandbox(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	s, fake, plan := dramaAudioFixture(t)
	s = newService(s.repo, t.TempDir())
	wav := filepath.Join(t.TempDir(), "tts.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000", "-t", "6", "-c:a", "pcm_s16le", "-y", wav).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v %s", err, out)
	}
	data, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/audio/speech") {
			t.Errorf("unexpected TTS path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["input"] != plan.Tracks[0].Text || body["voice"] != "alloy" {
			t.Error("TTS lost dialogue or voice")
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(data)
	}))
	defer upstream.Close()
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	if err := fake.db.Model(&model.ModelChannel{}).Where("id = ?", "audio-ch").Updates(map[string]any{"base_url": upstream.URL, "api_key": "sandbox-placeholder"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDramaSegmentAudio("usr-drama", plan); err != nil {
		t.Fatal(err)
	}
	req := MutateDramaAudioCueRequest{DramaAudioScope: plan.DramaAudioScope, ExpectedAudioRevision: 1, ExpectedShotRevision: plan.ExpectedShotRevision, CueID: plan.Tracks[0].ID, MaxCostMicrocredits: 100, IdempotencyKey: "tts-sandbox"}
	if _, err := s.MutateDramaAudioCue("usr-drama", "generate", req); err != nil {
		t.Fatal(err)
	}
	task, err := s.repo.ClaimNextTask("audio-sandbox-worker", 45*time.Second)
	if err != nil || task == nil {
		t.Fatalf("queue %v", err)
	}
	if err := newTaskWorkerCoordinator(s).processClaimedTask(task, nil); err != nil {
		t.Fatal(err)
	}
	finished, err := s.repo.Task(task.ID)
	if err != nil || finished.Status != model.TaskStatusSucceeded {
		t.Fatalf("worker %+v %v", finished, err)
	}
	resourceID := dramaTaskResource(finished)
	resource, err := s.repo.ResourceForUser("usr-drama", resourceID)
	if err != nil || resource.Kind != "audio" || resource.DurationMs < 5900 {
		t.Fatalf("stored audio %+v %v", resource, err)
	}
	req.ExpectedAudioRevision, req.IdempotencyKey = 2, "bind-worker-output"
	if _, err := s.MutateDramaAudioCue("usr-drama", "bind", req); err != nil {
		t.Fatal(err)
	}
	read, err := s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 0)
	if err != nil || read["version"].(*DramaAudioVersionView).Tracks[0].ResourceID != resourceID || read["qaStatus"] != "unreviewed" {
		t.Fatalf("bound audio %+v %v", read, err)
	}
	if calls != 1 {
		t.Fatalf("unexpected upstream requests %d", calls)
	}
}
