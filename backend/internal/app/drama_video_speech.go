package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yingce/backend/internal/model"
	"yingce/backend/internal/production"
	"yingce/backend/internal/repository"
)

type InspectDramaVideoSpeechRequest struct {
	DramaAudioScope
	VideoTakeID           string `json:"videoTakeId"`
	ExpectedShotRevision  string `json:"expectedShotRevision"`
	ExpectedAudioRevision int64  `json:"expectedAudioRevision"`
	StreamIndex           *int   `json:"streamIndex,omitempty"`
	IdempotencyKey        string `json:"idempotencyKey"`
}

// A native-video plan covers every shot, including windows where no words are expected.
// Its source is server-assigned; callers cannot use it to bypass independent-cue checks.
func nativeDramaAudioCues(target dramaAudioTarget) []DramaAudioCue {
	cues := make([]DramaAudioCue, 0, len(target.windows))
	for _, w := range target.windows {
		kind := "speech"
		if production.NormalizeSpeechText(w.Text) == "" {
			kind = "silence"
		}
		cues = append(cues, DramaAudioCue{DramaAudioCueInput: DramaAudioCueInput{ID: "shot-" + w.ShotID, ShotID: w.ShotID, Kind: kind, DialogueMode: w.DialogueMode, Speaker: w.Speaker, Text: w.Text, StartMs: w.StartMs, EndMs: w.EndMs}, Source: "native_video"})
	}
	return cues
}

func (s *Service) stageDramaMedia(ctx context.Context, userID, id, dir, name string) (string, string, error) {
	resource, body, err := s.OpenResource(userID, id)
	if err != nil {
		return "", "", err
	}
	defer body.Close()
	const limit int64 = 256 << 20
	if resource.Status != model.ResourceStatusReady || !isTranscribableMime(resource.MimeType) || resource.Size > limit {
		return "", "", BadAuthRequest("媒体须为已就绪的音视频，且不超过 256 MiB")
	}
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	path := filepath.Join(dir, name+extForMime(resource.MimeType))
	file, err := os.Create(path)
	if err != nil {
		return "", "", err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, limit+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || n > limit {
		return "", "", BadAuthRequest("媒体读取失败或超过检查大小上限")
	}
	return path, fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (s *Service) InspectDramaVideoSpeech(ctx context.Context, userID string, req InspectDramaVideoSpeechRequest) (map[string]any, error) {
	if replay, found, err := s.replayDramaMutation(userID, req.DramaID, req.EpisodeID, "audio.video-inspect", req.IdempotencyKey, req); found || err != nil {
		return replay, err
	}
	target, err := s.dramaAudioTarget(userID, req.DramaAudioScope)
	if err != nil {
		return nil, err
	}
	before, _, err := s.dramaAudioVersion(req.DramaID, target.segment.ID, 0)
	if err != nil {
		return nil, err
	}
	if err := checkDramaAudioRevision(target, before, req.ExpectedAudioRevision, req.ExpectedShotRevision); err != nil {
		return nil, err
	}
	if len(target.windows) == 0 || len(target.windows) > 100 || target.totalMs <= 0 || target.totalMs > 300000 {
		return nil, BadAuthRequest("整段检查支持 1–100 镜、最多 300 秒")
	}
	take, err := s.repo.DramaTake(req.DramaID, req.VideoTakeID)
	if err != nil || take == nil || take.Kind != "video" || take.OwnerID != target.segment.ID || take.ShotRevision != req.ExpectedShotRevision {
		return nil, BadAuthRequest("须指定与现行分镜一致的本段 videoTakeId")
	}
	resource, err := s.repo.ResourceForUser(userID, take.ResourceID)
	if err != nil {
		return nil, err
	}
	if resource.Kind != "video" || !strings.HasPrefix(resource.MimeType, "video/") {
		return nil, BadAuthRequest("videoTakeId 必须指向真实视频资源")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("", "yingce-video-speech-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	input, hash, err := s.stageDramaMedia(ctx, userID, take.ResourceID, dir, "video")
	if err != nil {
		return nil, err
	}
	cues := nativeDramaAudioCues(target)
	inspections := make([]DramaAudioInspection, 0, len(cues))
	for _, cue := range cues {
		initial := DramaAudioInspection{Source: "video", ResourceID: take.ResourceID, ResourceSHA256: hash, SourceOffsetMs: cue.StartMs, StartMs: cue.StartMs, EndMs: cue.EndMs, MeasurementStatus: "unknown", Streams: []DramaMediaStream{}, Alignment: production.AlignAudioText(cue.Text, nil, cue.StartMs, cue.EndMs), SpeakerStatus: "unverified", MouthStatus: "unverified"}
		inspection, err := s.inspectDramaAudioFile(ctx, input, dir, cue, cue.StartMs, req.StreamIndex, initial)
		if err != nil {
			return nil, err
		}
		inspections = append(inspections, inspection)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "audio.video-inspect", req.IdempotencyKey, req, func(local *Service) (any, error) {
		fresh, err := local.dramaAudioTarget(userID, req.DramaAudioScope)
		if err != nil {
			return nil, err
		}
		current, _, err := local.dramaAudioVersion(req.DramaID, fresh.segment.ID, 0)
		if err != nil {
			return nil, err
		}
		if err := checkDramaAudioRevision(fresh, current, req.ExpectedAudioRevision, req.ExpectedShotRevision); err != nil {
			return nil, err
		}
		saved, err := local.saveDramaAudioVersion(userID, req.DramaAudioScope, fresh, current, take.ID, take.ResourceID, cues)
		if err != nil {
			return nil, err
		}
		version := saved.(DramaAudioVersionView)
		results := []map[string]any{}
		for i, inspection := range inspections {
			check, err := local.appendDramaAudioCheck(userID, &version, cues[i].ID, "asr_video", inspection.Alignment.Status, "whisper_cpp", take.ResourceID, hash, inspection, "")
			if err != nil {
				return nil, err
			}
			results = append(results, map[string]any{"shotId": cues[i].ShotID, "cueId": cues[i].ID, "expectedSpeaker": cues[i].Speaker, "dialogueMode": cues[i].DialogueMode, "mouthRule": fresh.windows[i].MouthRule, "inspection": inspection, "check": check})
		}
		qa, err := local.dramaAudioQA(&version, fresh)
		if err != nil {
			return nil, err
		}
		return map[string]any{"version": version, "shotResults": results, "qa": qa, "accepted": false, "automaticSpeakerCheckAvailable": false, "automaticMouthCheckAvailable": false, "generationSubmitted": false}, nil
	})
}

// replayDramaMutation avoids rerunning media work after a successful version allocation.
func (s *Service) replayDramaMutation(userID, projectID, episodeID, command, key string, input any) (map[string]any, bool, error) {
	if _, _, err := s.dramaForUser(userID, projectID); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		return nil, false, BadAuthRequest("需要 1–200 字节的 idempotencyKey")
	}
	data, found, err := s.repo.DramaMutationReplay(projectID, episodeID, dramaContentRevision([]string{userID, projectID, episodeID, command, key}), dramaContentRevision(input))
	if errors.Is(err, repository.ErrDramaIdempotencyConflict) {
		return nil, false, NewAppError(409, "幂等键已用于不同请求")
	}
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, false, err
	}
	return value, true, nil
}
