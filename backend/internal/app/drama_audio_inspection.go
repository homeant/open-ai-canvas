package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yingce/backend/internal/model"
	"yingce/backend/internal/production"
)

type InspectDramaAudioRequest struct {
	DramaAudioScope
	AudioRevision   int64  `json:"audioRevision"`
	CueID           string `json:"cueId"`
	Source          string `json:"source" jsonschema:"cue 检查绑定音频，video 检查该版本视频中的实际音轨"`
	StreamIndex     *int   `json:"streamIndex,omitempty"`
	ExpectedCheckID string `json:"expectedCheckId"`
	IdempotencyKey  string `json:"idempotencyKey"`
}

type DramaAudioInspection struct {
	Source            string                    `json:"source"`
	ResourceID        string                    `json:"resourceId"`
	ResourceSHA256    string                    `json:"resourceSha256,omitempty"`
	MeasurementStatus string                    `json:"measurementStatus"`
	HasAudioTrack     *bool                     `json:"hasAudioTrack"`
	Streams           []DramaMediaStream        `json:"streams"`
	StreamIndex       *int                      `json:"streamIndex"`
	DurationMs        int64                     `json:"durationMs"`
	SourceOffsetMs    int64                     `json:"sourceOffsetMs"`
	StartMs           int64                     `json:"startMs"`
	EndMs             int64                     `json:"endMs"`
	Alignment         production.AudioAlignment `json:"alignment"`
	Error             string                    `json:"error,omitempty"`
	SpeakerStatus     string                    `json:"speakerStatus"`
	MouthStatus       string                    `json:"mouthStatus"`
}

// InspectDramaAudio reads bytes from an owned resource; client-supplied transcript/QA cannot enter this path.
func (s *Service) InspectDramaAudio(ctx context.Context, userID string, req InspectDramaAudioRequest) (map[string]any, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 200 {
		return nil, BadAuthRequest("媒体检查需要幂等键")
	}
	_, version, cue, err := s.loadDramaAudioCheckTarget(userID, req.DramaAudioScope, req.AudioRevision, req.CueID)
	if err != nil {
		return nil, err
	}
	if cue == nil || cue.Kind != "speech" || (req.Source != "cue" && req.Source != "video") {
		return nil, BadAuthRequest("请指定 speech 片段以及 cue/video 检查来源")
	}
	resourceID, offset := cue.ResourceID, cue.SourceOffsetMs
	if req.Source == "video" {
		resourceID, offset = version.VideoResourceID, cue.StartMs
	}
	if resourceID == "" {
		return nil, BadAuthRequest("该音轨版本没有绑定待检查的实际资源")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	inspection, err := s.inspectDramaAudioResource(ctx, userID, resourceID, req.Source, *cue, offset, req.StreamIndex)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "audio.inspect", req.IdempotencyKey, req, func(local *Service) (any, error) {
		_, current, _, err := local.loadDramaAudioCheckTarget(userID, req.DramaAudioScope, req.AudioRevision, req.CueID)
		if err != nil {
			return nil, err
		}
		if current.ID != version.ID {
			return nil, dramaMCPConflict(map[string]any{"versionId": current.ID})
		}
		return local.appendDramaAudioCheck(userID, current, cue.ID, "asr_"+req.Source, inspection.Alignment.Status, "whisper_cpp", resourceID, inspection.ResourceSHA256, inspection, req.ExpectedCheckID)
	})
}

func (s *Service) inspectDramaAudioResource(ctx context.Context, userID, resourceID, source string, cue DramaAudioCue, offset int64, streamIndex *int) (DramaAudioInspection, error) {
	result := DramaAudioInspection{Source: source, ResourceID: resourceID, SourceOffsetMs: offset, StartMs: cue.StartMs, EndMs: cue.EndMs, MeasurementStatus: "unknown", Streams: []DramaMediaStream{}, Alignment: production.AlignAudioText(cue.Text, nil, cue.StartMs, cue.EndMs), SpeakerStatus: "unverified", MouthStatus: "unverified"}
	resource, body, err := s.OpenResource(userID, resourceID)
	if err != nil {
		return result, err
	}
	defer body.Close()
	if resource.Status != model.ResourceStatusReady || !isTranscribableMime(resource.MimeType) {
		return result, BadAuthRequest("检查资源不是已就绪的音视频")
	}
	const limit int64 = 256 << 20
	if resource.Size > limit {
		return result, BadAuthRequest("媒体超过音轨检查大小上限")
	}
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	dir, err := os.MkdirTemp("", "yingce-audio-check-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	file, err := os.Create(filepath.Join(dir, "input"+extForMime(resource.MimeType)))
	if err != nil {
		return result, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, limit+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || n > limit {
		return result, BadAuthRequest("媒体读取失败或超过大小上限")
	}
	result.ResourceSHA256 = fmt.Sprintf("%x", hash.Sum(nil))
	info, err := probeMediaFile(ctx, file.Name())
	if err != nil {
		result.Error = "媒体探测失败或 ffprobe 不可用；音轨和语音均未验证"
		return result, nil
	}
	result.MeasurementStatus, result.Streams, result.DurationMs = "measured", info.Streams, info.DurationMs
	audioIndexes := []int{}
	for _, stream := range info.Streams {
		if stream.CodecType == "audio" {
			audioIndexes = append(audioIndexes, stream.Index)
		}
	}
	hasAudio := len(audioIndexes) > 0
	result.HasAudioTrack = &hasAudio
	if !hasAudio {
		result.Alignment.Status, result.Error = "mismatch", "该视频没有音轨，不能完成对白／独白"
		return result, nil
	}
	selected := audioIndexes[0]
	if streamIndex == nil && len(audioIndexes) != 1 {
		result.Error = "有多条音轨，请明确 streamIndex 后检查"
		return result, nil
	}
	if streamIndex != nil {
		found := false
		for _, index := range audioIndexes {
			if index == *streamIndex {
				selected, found = index, true
			}
		}
		if !found {
			return result, BadAuthRequest("streamIndex 不是实际音频流")
		}
	}
	result.StreamIndex = &selected
	if offset+cue.EndMs-cue.StartMs > info.DurationMs {
		result.Alignment.Status, result.Error = "mismatch", "实际媒体时长不足以覆盖镜头时间窗"
		return result, nil
	}
	baseURL := strings.TrimSpace(os.Getenv(whisperLangEnv))
	if baseURL == "" {
		result.Error = "ASR 未配置：CANVAS_WHISPER_BASE_URL；音轨存在不代表文本或人声合格"
		return result, nil
	}
	if err := s.RequireFeature(FeatureTimelineTranscription); err != nil {
		result.Error = "ASR 功能未开放；文本未验证"
		return result, nil
	}
	wav := filepath.Join(dir, "excerpt.wav")
	seconds := func(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }
	// Only the requested time window is recognized; expected dialogue is never sent as an ASR prompt.
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-y", "-i", file.Name(), "-ss", seconds(offset), "-t", seconds(cue.EndMs-cue.StartMs), "-map", "0:"+strconv.Itoa(selected), "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav)
	if _, err := cmd.CombinedOutput(); err != nil {
		result.Error = "音频片段解码失败或 ffmpeg 不可用；语音未验证"
		return result, nil
	}
	segments, _, err := newWhisperClient(baseURL).transcribe(ctx, wav, "zh")
	if err != nil {
		result.Error = "ASR 识别失败或未返回文本；语音内容未验证"
		return result, nil
	}
	phrases := make([]production.AudioPhrase, 0, len(segments))
	for _, segment := range segments {
		phrases = append(phrases, production.AudioPhrase{StartMs: segment.StartMs, EndMs: segment.EndMs, Text: segment.Text, Words: segment.Words})
	}
	result.Alignment = production.AlignAudioText(cue.Text, phrases, cue.StartMs, cue.EndMs)
	return result, nil
}
