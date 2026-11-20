package app

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"yingce/backend/internal/model"
)

func TestDramaAudioRenderDiscardsNativeAndAlignsIndependentAudio(t *testing.T) {
	s, fake, plan, _ := dramaAudioMediaFixture(t)
	plan.Tracks[0].StartMs, plan.Tracks[0].EndMs, plan.Tracks[0].SourceOffsetMs = 1000, 3000, 500
	if _, err := s.SaveDramaSegmentAudio("usr-drama", plan); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(s.dataDir, "resources", "independent.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=660:sample_rate=16000:duration=0.5", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=16000:duration=5.5", "-filter_complex", "[0:a][1:a]concat=n=2:v=0:a=1[a]", "-map", "[a]", "-c:a", "pcm_s16le", "-y", audio).CombinedOutput(); err != nil {
		t.Fatalf("audio %v %s", err, out)
	}
	id := fake.resource("usr-drama", "audio", "audio/wav")
	stat, _ := os.Stat(audio)
	if err := fake.db.Model(&model.Resource{}).Where("id = ?", id).Updates(map[string]any{"provider": "local", "object_key": "independent.wav", "duration_ms": 6000, "size": stat.Size()}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.MutateDramaAudioCue("usr-drama", "bind", MutateDramaAudioCueRequest{DramaAudioScope: plan.DramaAudioScope, ExpectedAudioRevision: 1, ExpectedShotRevision: plan.ExpectedShotRevision, CueID: plan.Tracks[0].ID, ResourceID: id, IdempotencyKey: "bind-render"}); err != nil {
		t.Fatal(err)
	}
	target, err := s.dramaAudioTarget("usr-drama", plan.DramaAudioScope)
	if err != nil {
		t.Fatal(err)
	}
	before := target.segment
	req := RenderDramaAudioRequest{DramaAudioScope: plan.DramaAudioScope, ExpectedAudioRevision: 2, ExpectedShotRevision: plan.ExpectedShotRevision, IdempotencyKey: "render"}
	if _, err := s.RenderDramaSegmentAudio(context.Background(), "usr-drama", req); err == nil {
		t.Fatal("implicit loss of native sound allowed")
	}
	req.NativeAudioMode = "discard"
	if err := fake.db.Model(&model.DramaTake{}).Where("id = ?", plan.VideoTakeID).Update("shot_revision", "old").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenderDramaSegmentAudio(context.Background(), "usr-drama", req); err == nil {
		t.Fatal("old source video relabeled as current")
	}
	if err := fake.db.Model(&model.DramaTake{}).Where("id = ?", plan.VideoTakeID).Update("shot_revision", plan.ExpectedShotRevision).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenderDramaSegmentAudio(context.Background(), "other", req); err == nil {
		t.Fatal("foreign render")
	}
	result, err := s.RenderDramaSegmentAudio(context.Background(), "usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	if result["nativeAudioDiscarded"] != true || result["accepted"] != false || result["adopted"] != false {
		t.Fatalf("result %+v", result)
	}
	replay, err := s.RenderDramaSegmentAudio(context.Background(), "usr-drama", req)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("render replay %v", err)
	}
	dir := t.TempDir()
	rendered, _, err := s.stageDramaMedia(context.Background(), "usr-drama", result["videoResourceId"].(string), dir, "rendered")
	if err != nil {
		t.Fatal(err)
	}
	// Decode the real final AAC: the leading gap must be silent, and the independent
	// 880 Hz fixture must replace (not mix with) the source video's 440 Hz fixture.
	measure := func(start string) (float64, float64, float64, float64) {
		data, err := exec.Command("ffmpeg", "-v", "error", "-i", rendered, "-ss", start, "-t", "0.5", "-vn", "-ac", "1", "-ar", "16000", "-f", "f32le", "pipe:1").Output()
		if err != nil {
			t.Fatal(err)
		}
		var energy, re440, im440, re660, im660, re880, im880 float64
		for i := 0; i+4 <= len(data); i += 4 {
			sample := float64(math.Float32frombits(binary.LittleEndian.Uint32(data[i : i+4])))
			n := float64(i / 4)
			energy += sample * sample
			re440 += sample * math.Cos(2*math.Pi*440*n/16000)
			im440 += sample * math.Sin(2*math.Pi*440*n/16000)
			re660 += sample * math.Cos(2*math.Pi*660*n/16000)
			im660 += sample * math.Sin(2*math.Pi*660*n/16000)
			re880 += sample * math.Cos(2*math.Pi*880*n/16000)
			im880 += sample * math.Sin(2*math.Pi*880*n/16000)
		}
		return energy, math.Hypot(re440, im440), math.Hypot(re660, im660), math.Hypot(re880, im880)
	}
	gap, _, _, _ := measure("0.2")
	energy, native, sourceLead, independent := measure("1.1")
	if gap > 0.01 || energy < 1 || independent < native*20 || independent < sourceLead*20 {
		t.Fatalf("native audio retained or timing wrong: gap=%f energy=%f 440=%f 660=%f 880=%f", gap, energy, native, sourceLead, independent)
	}
	read, err := s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 0)
	if err != nil || read["qaStatus"] != "unreviewed" || read["version"].(*DramaAudioVersionView).Revision != 3 {
		t.Fatalf("new video reused QA %+v %v", read, err)
	}
	after, err := s.repo.DramaSegment(plan.DramaID, target.segment.ID)
	if err != nil || !reflect.DeepEqual(before, *after) {
		t.Fatal("render changed formal video")
	}
	take, err := s.repo.DramaTake(plan.DramaID, result["videoTakeId"].(string))
	if err != nil || take.ShotRevision != plan.ExpectedShotRevision || take.ResourceID == before.VideoResourceID {
		t.Fatalf("missing candidate take %+v %v", take, err)
	}
}
