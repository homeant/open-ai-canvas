package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yingce/backend/internal/model"
)

type RenderDramaAudioRequest struct {
	DramaAudioScope
	ExpectedAudioRevision int64  `json:"expectedAudioRevision"`
	ExpectedShotRevision  string `json:"expectedShotRevision"`
	NativeAudioMode       string `json:"nativeAudioMode" jsonschema:"必须明确 discard；丢弃原生全部音轨，包括原生音乐和环境音"`
	IdempotencyKey        string `json:"idempotencyKey"`
}

// The original audio stream is never mapped. Only explicitly bound independent
// tracks enter the mix; speech separation or preservation of native music is not implied.
func (s *Service) RenderDramaSegmentAudio(ctx context.Context, userID string, req RenderDramaAudioRequest) (map[string]any, error) {
	if replay, found, err := s.replayDramaMutation(userID, req.DramaID, req.EpisodeID, "audio.render", req.IdempotencyKey, req); found || err != nil {
		return replay, err
	}
	if req.NativeAudioMode != "discard" {
		return nil, BadAuthRequest("须明确 nativeAudioMode=discard；原生对白、音乐和环境音会全部丢弃")
	}
	target, err := s.dramaAudioTarget(userID, req.DramaAudioScope)
	if err != nil {
		return nil, err
	}
	version, _, err := s.dramaAudioVersion(req.DramaID, target.segment.ID, 0)
	if err != nil {
		return nil, err
	}
	if err := checkDramaAudioRevision(target, version, req.ExpectedAudioRevision, req.ExpectedShotRevision); err != nil {
		return nil, err
	}
	if version == nil || version.ShotRevision != req.ExpectedShotRevision || version.VideoResourceID == "" || target.totalMs <= 0 || target.totalMs > 300000 || len(version.Tracks) > 100 {
		return nil, BadAuthRequest("须保存与现行分镜一致且绑定视频的音轨版本；最多 300 秒／100 片段")
	}
	sourceTake, err := s.repo.DramaTake(req.DramaID, version.VideoTakeID)
	if err != nil {
		return nil, err
	}
	if sourceTake.Kind != "video" || sourceTake.OwnerID != target.segment.ID || sourceTake.ResourceID != version.VideoResourceID || sourceTake.ShotRevision != req.ExpectedShotRevision {
		return nil, BadAuthRequest("源视频版本与音轨／现行分镜不一致，不能给替换产物标记新分镜版本")
	}
	if issues := dramaAudioCoverage(target, version.Tracks); len(issues) > 0 {
		return nil, BadAuthRequest(strings.Join(issues, "；"))
	}
	inputs := []DramaAudioCueInput{}
	for _, cue := range version.Tracks {
		if cue.Source == "native_video" || cue.ResourceID == "" || (cue.Kind != "speech" && cue.Kind != "sfx") {
			return nil, BadAuthRequest("全部片段须绑定独立音频；不能把原生视频音轨用作替换源")
		}
		inputs = append(inputs, cue.DramaAudioCueInput)
	}
	if err := validateDramaAudioCues(inputs, target); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("", "yingce-audio-render-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	video, videoHash, err := s.stageDramaMedia(ctx, userID, version.VideoResourceID, dir, "video")
	if err != nil {
		return nil, err
	}
	info, err := probeMediaFile(ctx, video)
	if err != nil {
		return nil, err
	}
	if info.Width <= 0 || info.Height <= 0 || info.DurationMs < target.totalMs {
		return nil, BadAuthRequest("实际视频无法覆盖分镜，或没有可用画面流")
	}
	ffmpeg, err := renderFfmpegBinary()
	if err != nil {
		return nil, err
	}
	args := []string{"-nostdin", "-v", "error", "-y", "-protocol_whitelist", "file,pipe", "-i", video}
	filters := []string{"anullsrc=r=48000:cl=stereo,atrim=duration=" + audioSeconds(target.totalMs) + "[base]"}
	labels := []string{"[base]"}
	sources := []map[string]any{{"resourceId": version.VideoResourceID, "sha256": videoHash, "role": "picture_only"}}
	stagedBytes, err := os.Stat(video)
	if err != nil {
		return nil, err
	}
	inputBytes := stagedBytes.Size()
	for i, cue := range version.Tracks {
		path, hash, err := s.stageDramaMedia(ctx, userID, cue.ResourceID, dir, fmt.Sprintf("audio-%d", i))
		if err != nil {
			return nil, err
		}
		stat, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		inputBytes += stat.Size()
		if inputBytes > 512<<20 {
			return nil, BadAuthRequest("合成输入总量不能超过 512 MiB")
		}
		media, err := probeMediaFile(ctx, path)
		if err != nil {
			return nil, err
		}
		audioStreams := []int{}
		for _, stream := range media.Streams {
			if stream.CodecType == "audio" {
				audioStreams = append(audioStreams, stream.Index)
			}
		}
		if len(audioStreams) != 1 || media.Width != 0 || media.DurationMs < cue.SourceOffsetMs+cue.EndMs-cue.StartMs {
			return nil, BadAuthRequest("独立音频须只有一条音频流、无视频且实测时长覆盖片段")
		}
		args = append(args, "-protocol_whitelist", "file,pipe", "-i", path)
		label := fmt.Sprintf("[cue%d]", i)
		filters = append(filters, fmt.Sprintf("[%d:%d]atrim=start=%s:duration=%s,asetpts=PTS-STARTPTS,aresample=48000,adelay=%d:all=1%s", i+1, audioStreams[0], audioSeconds(cue.SourceOffsetMs), audioSeconds(cue.EndMs-cue.StartMs), cue.StartMs, label))
		labels = append(labels, label)
		sources = append(sources, map[string]any{"cueId": cue.ID, "resourceId": cue.ResourceID, "sha256": hash, "startMs": cue.StartMs, "endMs": cue.EndMs, "sourceOffsetMs": cue.SourceOffsetMs})
	}
	filters = append(filters, strings.Join(labels, "")+"amix=inputs="+strconv.Itoa(len(labels))+":duration=first:normalize=0[mixed]")
	output := filepath.Join(dir, "aligned.mp4")
	args = append(args, "-filter_complex", strings.Join(filters, ";"), "-map", "0:v:0", "-map", "[mixed]", "-c:v", "copy", "-c:a", "aac", "-t", audioSeconds(target.totalMs), "-map_metadata", "0", "-movflags", "+faststart", output)
	if _, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, NewAppError(422, "独立音轨合成失败；未替换原视频")
	}
	measured, err := probeMediaFile(ctx, output)
	if err != nil {
		return nil, err
	}
	if measured.Width <= 0 || measured.Height <= 0 || measured.DurationMs < target.totalMs-50 {
		return nil, NewAppError(422, "合成视频未覆盖完整镜头时窗")
	}
	file, err := os.Open(output)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	resource, err := s.UploadResourceFile(userID, "aligned.mp4", stat.Size(), "video", measured.Width, measured.Height, measured.DurationMs, file, "drama-audio-render-"+dramaContentRevision([]any{userID, req}))
	if err != nil {
		return nil, err
	}
	return s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "audio.render", req.IdempotencyKey, req, func(local *Service) (any, error) {
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
		origin, _ := json.Marshal(map[string]any{"audioReplacement": map[string]any{"nativeAudioMode": "discard", "sourceVideoTakeId": version.VideoTakeID, "sourceAudioVersionId": version.ID, "sourceAudioRevision": version.Revision, "sources": sources}})
		take := model.DramaTake{ID: newID(), ProjectID: req.DramaID, EpisodeID: req.EpisodeID, OwnerID: fresh.segment.ID, Kind: "video", ResourceID: resource.ID, DurationMs: measured.DurationMs, ShotRevision: req.ExpectedShotRevision, GenerationJSON: string(origin), Prompt: "独立音轨对齐（原生音轨已丢弃，待验收）", CreatedAt: time.Now()}
		if err := local.repo.CreateDramaTake(&take); err != nil {
			return nil, err
		}
		saved, err := local.saveDramaAudioVersion(userID, req.DramaAudioScope, fresh, current, take.ID, resource.ID, version.Tracks)
		if err != nil {
			return nil, err
		}
		return map[string]any{"version": saved, "videoTakeId": take.ID, "videoResourceId": resource.ID, "nativeAudioDiscarded": true, "sourceAudioRevision": version.Revision, "sources": sources, "qaStatus": "unreviewed", "accepted": false, "adopted": false, "generationSubmitted": false}, nil
	})
}

func audioSeconds(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }
