package app

import (
	"context"
	"errors"
	"strings"

	"yingce/backend/internal/model"
)

func (s *Service) InspectDramaSegmentVersionMedia(ctx context.Context, userID, projectID, episodeID string, index int, takeID string) (map[string]any, error) {
	versions, err := s.DramaSegmentVideoVersions(userID, projectID, episodeID, index)
	if err != nil {
		return nil, err
	}
	var target *DramaSegmentVideoVersion
	for i := range versions.Versions {
		if (takeID != "" && versions.Versions[i].ID == takeID) || (takeID == "" && versions.Versions[i].Current) {
			target = &versions.Versions[i]
			break
		}
	}
	if target == nil {
		return nil, NotFound("本段视频版本不存在")
	}
	resource, err := s.repo.ResourceForUser(userID, target.ResourceID)
	if err != nil {
		return nil, err
	}
	info, err := s.probeStoredResourceMedia(ctx, resource)
	if err != nil {
		reason := "媒体探测失败或文件头不足，未取得可靠流信息"
		if errors.Is(err, errMediaProbeUnavailable) {
			reason = "服务端未安装 ffprobe"
		}
		return map[string]any{"takeId": target.ID, "resourceId": target.ResourceID, "measurementStatus": "unknown", "measurementError": reason, "hasAudioTrack": nil, "hasSpeech": nil, "usableDialogueAudio": "unverified"}, nil
	}
	hasAudio := false
	for _, stream := range info.Streams {
		if stream.CodecType == "audio" {
			hasAudio = true
		}
	}
	return map[string]any{"takeId": target.ID, "resourceId": target.ResourceID, "shotRevision": target.ShotRevision, "promptRevision": target.PromptRevision, "scriptMatch": target.ScriptMatch, "measurementStatus": "measured", "width": info.Width, "height": info.Height, "durationMs": info.DurationMs, "streams": info.Streams, "hasAudioTrack": hasAudio, "hasSpeech": nil, "usableDialogueAudio": "unverified", "soundSource": "unverified", "note": "流信息由 ffprobe 实测；有音轨不证明有人声，语音活动也不证明台词、说话人或口型合格。"}, nil
}

// PreviewDramaSegmentGeneration uses the same prepared request and intent conversion as submission.
func (s *Service) PreviewDramaSegmentGeneration(userID, projectID, episodeID string, index int) (map[string]any, error) {
	detail, err := s.DramaSegmentDetail(userID, projectID, episodeID, index)
	if err != nil {
		return nil, err
	}
	segment := detail.Segment
	request := segment.Request
	config := dramaSegmentGenerationConfig(request)
	images := []any{}
	sentImages := []map[string]any{}
	textOnlyReferences := []DramaSegmentReference{}
	if request.FirstFrameResourceID != "" {
		images = append(images, map[string]any{"storageKey": "resource:" + request.FirstFrameResourceID})
		sentImages = append(sentImages, map[string]any{"role": "first_frame", "resourceId": request.FirstFrameResourceID, "shotId": request.FirstFrameShotID, "inputOrder": 0})
	}
	for _, ref := range request.References {
		if ref.Sent && ref.ResourceID != "" {
			images = append(images, map[string]any{"storageKey": "resource:" + ref.ResourceID})
			sentImages = append(sentImages, map[string]any{"role": "reference_image", "resourceId": ref.ResourceID, "castId": ref.CastID, "label": ref.Label, "kind": ref.Kind, "name": ref.Name, "shotId": ref.ShotID, "usageRole": ref.Role, "inputOrder": ref.InputOrder, "required": ref.Required})
		} else {
			textOnlyReferences = append(textOnlyReferences, ref)
		}
	}
	shotBindings := []map[string]any{}
	for _, shot := range detail.Shots {
		shotBindings = append(shotBindings, map[string]any{"shotId": shot.ID, "scene": shot.Scene, "sceneCastId": shot.SceneCastID, "castIds": append([]string{}, decodeStringList(shot.CastIDsJSON)...), "propIds": append([]string{}, decodeStringList(shot.PropIDsJSON)...), "lookCastIds": append([]string{}, decodeStringList(shot.LookCastIDsJSON)...)})
	}
	audios := []any{}
	for _, ref := range request.AudioReferences {
		if ref.Sent {
			audios = append(audios, map[string]any{"storageKey": "resource:" + ref.ResourceID})
		}
	}
	input := map[string]any{"mode": "video", "config": config, "referenceImages": images, "referenceAudios": audios}
	intent := ModelRequestIntentFromTaskInput(input, "canvas_video", request.Operation)
	var quote *LogicalModelQuote
	var quoteErr error
	if segment.VideoModel.LogicalModelID != "" {
		quote, quoteErr = s.QuoteLogicalModel(userID, segment.VideoModel.LogicalModelID, intent)
	} else {
		quote, quoteErr = s.QuoteChannelModel(userID, ChannelModelQuoteRequest{ChannelID: segment.VideoModel.ChannelID, ModelKey: segment.VideoModel.ModelKey, Intent: intent})
	}
	quoteReason := ""
	if quoteErr != nil {
		quoteReason = quoteErr.Error()
	}
	blockers := dramaPromptConflicts(segment, segment.VideoPrompt)
	if segment.PromptSeconds > 0 && segment.PromptSeconds != request.Seconds {
		blockers = append(blockers, "生效提示词时长档位与当前模型不一致")
	}
	if request.PreparationError != "" {
		blockers = append(blockers, request.PreparationError)
	}
	if segment.VideoPrompt == "" {
		blockers = append(blockers, "没有生效提示词")
	}
	if segment.PromptStatus == model.DramaPromptConflict || segment.PromptStatus == model.DramaPromptReview || segment.PromptStatus == "running" || segment.PromptStatus == "queued" || segment.PromptStatus == "failed" {
		blockers = append(blockers, "提示词尚未确认")
	}
	if segment.PromptShotRevision != "" && segment.PromptShotRevision != segment.ShotRevision {
		blockers = append(blockers, "提示词与现行分镜不一致")
	}
	if request.FirstFramePending {
		blockers = append(blockers, "上一段首帧尚未就绪")
	}
	if segment.VideoWait != "" {
		blockers = append(blockers, segment.VideoWait)
	}
	if quoteErr != nil {
		blockers = append(blockers, "当前规格不可报价: "+quoteReason)
	}
	needsVoice := false
	for _, shot := range detail.Shots {
		if shot.Dialogue != "" && shot.DialogueMode != "silent" {
			needsVoice = true
		}
	}
	return map[string]any{"segmentIndex": index, "segmentRevision": segment.Revision, "shotRevision": segment.ShotRevision, "episodeRevision": detail.EpisodeRevision, "referenceMode": segment.ReferenceMode, "shotBindings": shotBindings, "sentReferenceImages": sentImages, "textOnlyReferences": textOnlyReferences, "promptRevision": segment.PromptRevision, "model": segment.VideoModel, "request": request, "fullPrompt": strings.TrimSpace(request.Header + "\n\n" + segment.VideoPrompt), "intent": intent, "quote": quote, "unquotableReason": quoteReason, "blockers": blockers, "canSubmit": len(blockers) == 0, "sound": dramaSegmentSoundContract(request, needsVoice)}, nil
}
