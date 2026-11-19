package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDramaInsertedInformationShotDefaultsSilent(t *testing.T) {
	s, _, project, episode, before := mcpEditingFixture(t)
	one := 1.0
	text, speaker, explicit := "够了。", "陈启", "dialogue"
	req := EditDramaShotsRequest{ExpectedEpisodeRevision: before.EpisodeRevision, IdempotencyKey: "insert-info", Edits: []DramaShotEdit{
		{Operation: "insert_after", ShotID: before.Shots[0].ID, NewShotID: "clock", UpdateDramaShotRequest: UpdateDramaShotRequest{DurationSeconds: &one}},
		{Operation: "insert_after", ShotID: "clock", NewShotID: "spoken", UpdateDramaShotRequest: UpdateDramaShotRequest{DurationSeconds: &one, Speaker: &speaker, Dialogue: &text}},
		{Operation: "insert_after", ShotID: "spoken", NewShotID: "explicit", DialogueMode: &explicit, UpdateDramaShotRequest: UpdateDramaShotRequest{DurationSeconds: &one}},
	}}
	if _, err := s.EditDramaShots("usr-drama", project, episode, req); err != nil {
		t.Fatal(err)
	}
	shots, err := s.repo.DramaShots(episode)
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{}
	for _, shot := range shots {
		modes[shot.ID] = shot.DialogueMode
	}
	if modes["clock"] != "silent" || modes["spoken"] != "dialogue" || modes["explicit"] != "dialogue" {
		t.Fatalf("defaults %+v", modes)
	}
}

func TestDramaDigitalSilenceRejectsASRHallucination(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	s, fake, plan := dramaAudioFixture(t)
	s.dataDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(s.dataDir, "resources"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dataDir, "resources", "silent.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "anullsrc=r=16000:cl=mono", "-t", "4", "-c:a", "pcm_s16le", "-y", path).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	resourceID := fake.resource("usr-drama", "audio", "audio/wav")
	stat, _ := os.Stat(path)
	if err := fake.db.Table("resources").Where("id = ?", resourceID).Updates(map[string]any{"provider": "local", "object_key": "silent.wav", "duration_ms": 4000, "size": stat.Size()}).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	asr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"segments":[{"start":0,"end":1,"text":"这次，我不能再五五分。"}]}`))
	}))
	defer asr.Close()
	t.Setenv(whisperLangEnv, asr.URL)
	result, err := s.inspectDramaAudioResource(context.Background(), "usr-drama", resourceID, "cue", DramaAudioCue{DramaAudioCueInput: plan.Tracks[0]}, 0, nil)
	if err != nil || result.Alignment.Status != "mismatch" || result.Signal == nil || !result.Signal.DigitalSilence || calls != 0 {
		t.Fatalf("silence accepted %+v calls=%d %v", result, calls, err)
	}
}
