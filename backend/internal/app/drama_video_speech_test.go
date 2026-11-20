package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"yingce/backend/internal/model"
)

func dramaAudioMediaFixture(t *testing.T) (*Service, *fakeDramaBackend, SaveDramaAudioRequest, string) {
	t.Helper()
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " unavailable")
		}
	}
	s, fake, plan := dramaAudioFixture(t)
	s.dataDir = t.TempDir()
	root := filepath.Join(s.dataDir, "resources")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "native.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=180x320:rate=5", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000", "-t", "16", "-c:v", "mpeg4", "-c:a", "aac", "-y", path).CombinedOutput(); err != nil {
		t.Fatalf("media %v %s", err, out)
	}
	take, err := s.repo.DramaTake(plan.DramaID, plan.VideoTakeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fake.db.Model(&model.DramaTake{}).Where("id = ?", take.ID).Update("shot_revision", plan.ExpectedShotRevision).Error; err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(path)
	if err := fake.db.Model(&model.Resource{}).Where("id = ?", take.ResourceID).Updates(map[string]any{"provider": "local", "object_key": "native.mp4", "size": stat.Size(), "duration_ms": 16000}).Error; err != nil {
		t.Fatal(err)
	}
	return s, fake, plan, path
}

func TestDramaVideoSpeechWrongShotWindowsAndReplay(t *testing.T) {
	s, fake, plan, _ := dramaAudioMediaFixture(t)
	first := plan.Tracks[0].ShotID
	if err := fake.db.Model(&model.DramaShot{}).Where("id = ?", first).Updates(map[string]any{"speaker": "周牧", "dialogue_mode": "dialogue", "dialogue": "我们五五分。", "duration_ms": 7800}).Error; err != nil {
		t.Fatal(err)
	}
	for _, shot := range []model.DramaShot{
		{ID: "inner-shot", ProjectID: plan.DramaID, EpisodeID: plan.EpisodeID, SegmentIndex: 0, Position: 100, DurationMs: 7000, Speaker: "陈启", DialogueMode: "inner_monologue", Dialogue: "我不能再跟他五五分。"},
		{ID: "silent-shot", ProjectID: plan.DramaID, EpisodeID: plan.EpisodeID, SegmentIndex: 0, Position: 101, DurationMs: 1000, DialogueMode: "silent"},
	} {
		if err := fake.db.Create(&shot).Error; err != nil {
			t.Fatal(err)
		}
	}
	target, err := s.dramaAudioTarget("usr-drama", plan.DramaAudioScope)
	if err != nil {
		t.Fatal(err)
	}
	revision := dramaShotRevision(target.shots, 0)
	if err := fake.db.Model(&model.DramaTake{}).Where("id = ?", plan.VideoTakeID).Update("shot_revision", revision).Error; err != nil {
		t.Fatal(err)
	}
	before, err := s.repo.DramaSegment(plan.DramaID, target.segment.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	asr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("prompt") != "" {
			t.Error("script used as ASR hint")
		}
		text := "我不能再跟他五五分。" // Inner monologue spoken during Zhou's window.
		if calls == 2 {
			_, _ = w.Write([]byte(`{"segments":[]}`))
			return
		}
		if calls == 3 {
			text = "意外多出一句。"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"segments": []any{map[string]any{"start": 0, "end": 0.8, "text": text}}})
	}))
	defer asr.Close()
	t.Setenv(whisperLangEnv, asr.URL)
	req := InspectDramaVideoSpeechRequest{DramaAudioScope: plan.DramaAudioScope, VideoTakeID: plan.VideoTakeID, ExpectedShotRevision: revision, IdempotencyKey: "native-inspect"}
	if _, err := s.InspectDramaVideoSpeech(context.Background(), "other", req); err == nil {
		t.Fatal("foreign access")
	}
	result, err := s.InspectDramaVideoSpeech(context.Background(), "usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	rows := result["shotResults"].([]any)
	if len(rows) != 3 || calls != 3 || result["accepted"] != false {
		t.Fatalf("result %+v", result)
	}
	for i, want := range []string{"mismatch", "unknown", "mismatch"} {
		row := rows[i].(map[string]any)
		proof := row["inspection"].(map[string]any)
		if proof["alignment"].(map[string]any)["status"] != want || proof["mouthStatus"] != "unverified" || proof["speakerStatus"] != "unverified" || len(proof["resourceSha256"].(string)) != 64 {
			t.Fatalf("shot %d %+v", i, row)
		}
	}
	replayed, err := s.InspectDramaVideoSpeech(context.Background(), "usr-drama", req)
	if err != nil || !reflect.DeepEqual(result, replayed) || calls != 3 {
		t.Fatalf("replay reran ASR %v calls %d", err, calls)
	}
	read, err := s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 0)
	if err != nil || read["qaStatus"] != "rejected" {
		t.Fatalf("failure not exposed %+v %v", read, err)
	}
	qa := DramaAudioQARequest{DramaAudioScope: plan.DramaAudioScope, AudioRevision: 1, Layer: "final", Status: "approved", Reason: "ready video", ObservedEndMs: 15800, IdempotencyKey: "bad-final"}
	if _, err := s.RecordDramaAudioQA("usr-drama", qa); err == nil {
		t.Fatal("failed native audio approved")
	}
	qa.Layer, qa.CueID, qa.ObservedStartMs, qa.ObservedEndMs, qa.MouthState, qa.IdempotencyKey = "mouth", "shot-inner-shot", 7800, 14800, "speaking", "bad-mouth"
	if _, err := s.RecordDramaAudioQA("usr-drama", qa); err == nil {
		t.Fatal("inner monologue speaking approved")
	}
	qa.Status, qa.Reason, qa.IdempotencyKey = "rejected", "9 秒仍张嘴，独白要求闭口", "reject-mouth"
	if _, err := s.RecordDramaAudioQA("usr-drama", qa); err != nil {
		t.Fatal(err)
	}
	qa.Layer, qa.Status, qa.ObservedSpeaker, qa.IdempotencyKey = "speaker", "approved", "周牧", "wrong-speaker"
	if _, err := s.RecordDramaAudioQA("usr-drama", qa); err == nil {
		t.Fatal("wrong observed speaker approved")
	}
	after, err := s.repo.DramaSegment(plan.DramaID, target.segment.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection changed formal video")
	}
	req.IdempotencyKey = "stale"
	req.ExpectedShotRevision = "old"
	if _, err := s.InspectDramaVideoSpeech(context.Background(), "usr-drama", req); err == nil || calls != 3 {
		t.Fatal("stale revision did media work")
	}
}

func TestDramaVideoSpeechMatchRequiresManualEvidence(t *testing.T) {
	s, _, plan, _ := dramaAudioMediaFixture(t)
	asr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"segments": []any{map[string]any{"start": 0, "end": 1, "text": plan.Tracks[0].Text}}})
	}))
	defer asr.Close()
	t.Setenv(whisperLangEnv, asr.URL)
	req := InspectDramaVideoSpeechRequest{DramaAudioScope: plan.DramaAudioScope, VideoTakeID: plan.VideoTakeID, ExpectedShotRevision: plan.ExpectedShotRevision, IdempotencyKey: "matched-native"}
	result, err := s.InspectDramaVideoSpeech(context.Background(), "usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	if result["qa"].(map[string]any)["readyForFinalReview"] != false || result["accepted"] != false {
		t.Fatal("ASR inferred manual acceptance")
	}
	qa := DramaAudioQARequest{DramaAudioScope: plan.DramaAudioScope, AudioRevision: 1, Status: "approved", Reason: "隔离测试人工观察声明", ObservedEndMs: plan.Tracks[0].EndMs, MouthState: "closed", ObservedSpeaker: plan.Tracks[0].Speaker}
	for _, layer := range []string{"mouth", "speaker", "content", "picture", "final"} {
		qa.Layer, qa.IdempotencyKey, qa.CueID = layer, "native-review-"+layer, ""
		if layer == "mouth" || layer == "speaker" {
			qa.CueID = "shot-" + plan.Tracks[0].ShotID
		}
		if _, err := s.RecordDramaAudioQA("usr-drama", qa); err != nil {
			t.Fatalf("layer %s: %v", layer, err)
		}
	}
	read, err := s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 0)
	if err != nil || read["qa"].(map[string]any)["accepted"] != true {
		t.Fatalf("actual video evidence cannot be accepted %+v %v", read, err)
	}
}
