package app

import (
	"strings"

	"yingce/backend/internal/model"
	"yingce/backend/internal/production"
	"yingce/backend/internal/repository"
)

type DramaTimingIssue struct {
	Code         string `json:"code"`
	SegmentIndex int    `json:"segmentIndex"`
	ShotID       string `json:"shotId,omitempty"`
	CueID        string `json:"cueId,omitempty"`
	Detail       string `json:"detail"`
}

func timingAudioIssues(snapshot repository.DramaTimingSnapshot, before, after []DramaTimingWindow, sources map[string][]string, affected map[int]bool) ([]DramaTimingIssue, []map[string]any) {
	issues, invalidated := []DramaTimingIssue{}, []map[string]any{}
	texts := map[string]string{}
	oldWindows, newWindows := map[string]DramaTimingWindow{}, map[string]DramaTimingWindow{}
	for _, window := range before {
		oldWindows[window.ID] = window
	}
	for _, window := range after {
		for _, id := range sources[window.ID] {
			newWindows[id] = window
		}
	}
	latest := latestTimingAudio(snapshot)
	for _, segment := range snapshot.Segments {
		version, exists := latest[segment.ID]
		if !exists {
			continue
		}
		if affected[segment.SegmentIndex] {
			invalidated = append(invalidated, map[string]any{"segmentIndex": segment.SegmentIndex, "audioVersionId": version.ID, "audioRevision": version.Revision,
				"videoTakeId": version.VideoTakeID, "historicalVersionPreserved": true, "status": "requires_realignment_and_qa"})
		}
		for _, cue := range version.Tracks {
			old, oldExists := oldWindows[cue.ShotID]
			planned, plannedExists := newWindows[cue.ShotID]
			if !oldExists || !plannedExists || old.SegmentIndex != segment.SegmentIndex {
				issues = append(issues, DramaTimingIssue{"audio_target_not_in_segment", segment.SegmentIndex, cue.ShotID, cue.ID, "音轨镜头不属于保存版本的当前段，需重新对齐"})
				continue
			}
			if cue.Kind == "speech" {
				texts[planned.ID] += cue.Text
			}
			if planned.ID != cue.ShotID || planned.SegmentIndex != segment.SegmentIndex {
				issues = append(issues, DramaTimingIssue{"audio_target_merged_or_moved", segment.SegmentIndex, cue.ShotID, cue.ID, "原镜已合并；保留历史音频，须明确新 shotId、段与时间窗"})
			} else if old.SegmentStartMs != planned.SegmentStartMs {
				issues = append(issues, DramaTimingIssue{"audio_window_shifted", segment.SegmentIndex, cue.ShotID, cue.ID, "镜头在段内的起点已变化，音轨不能保持原时间码"})
			}
			if cue.StartMs < planned.SegmentStartMs || cue.EndMs > planned.SegmentEndMs || cue.EndMs <= cue.StartMs {
				issues = append(issues, DramaTimingIssue{"audio_outside_proposed_window", segment.SegmentIndex, cue.ShotID, cue.ID, "已保存音轨窗超出预演镜头窗；未自动裁切或变速"})
			}
			if cue.Kind == "speech" && strings.TrimSpace(cue.Text) == "" {
				issues = append(issues, DramaTimingIssue{"audio_speech_text_missing", segment.SegmentIndex, cue.ShotID, cue.ID, "对白音轨缺少原句，不能确认内容同步"})
			}
		}
		if version.ShotRevision != dramaShotRevision(snapshot.Shots, segment.SegmentIndex) {
			issues = append(issues, DramaTimingIssue{"audio_already_stale", segment.SegmentIndex, "", "", "音轨版本已与当前分镜内容不匹配"})
		}
	}
	for _, window := range after {
		if window.DialogueMode != "silent" && strings.TrimSpace(window.Dialogue) != "" && production.NormalizeSpeechText(texts[window.ID]) != production.NormalizeSpeechText(window.Dialogue) {
			issues = append(issues, DramaTimingIssue{"audio_text_does_not_cover_dialogue", window.SegmentIndex, window.ID, "", "已保存音轨计划没有完整覆盖合并后的原句；文本匹配仍不代表听审通过"})
		}
	}
	return issues, invalidated
}

// PreviewDramaTimingEdits only simulates shot edits. It never enters the mutation,
// task creation, resource binding or rendering paths, including on a valid plan.
func (s *Service) PreviewDramaTimingEdits(userID string, req PreviewDramaTimingRequest) (map[string]any, error) {
	if !req.DryRun || len(req.Edits) == 0 || len(req.Edits) > 100 {
		return nil, BadAuthRequest("本接口仅支持 dryRun=true 和 1–100 个镜头时长／相邻合并操作")
	}
	snapshot, err := s.readDramaTiming(userID, req.DramaTimingRequest)
	if err != nil {
		return nil, err
	}
	if err := validateTimingAudio(snapshot); err != nil {
		return nil, err
	}
	episodeRevision, timingRevision := dramaTimingRevisions(snapshot)
	if req.ExpectedEpisodeRevision == "" || req.ExpectedTimingRevision == "" || req.ExpectedEpisodeRevision != episodeRevision || req.ExpectedTimingRevision != timingRevision {
		return nil, dramaMCPConflict(map[string]any{"episodeRevision": episodeRevision, "timingRevision": timingRevision})
	}
	shots, sources, direct, err := applyTimingEdits(snapshot.Shots, req.Edits)
	if err != nil {
		return nil, err
	}
	settings := parseDramaSettings(snapshot.Production.SettingsJSON)
	before := dramaTimingWindows(snapshot.Shots, settings, nil)
	after := dramaTimingWindows(shots, settings, sources)
	invalidated := map[int]bool{}
	for index := range direct {
		invalidated[index] = true
		if !settings.parallelVideo() {
			for _, segment := range snapshot.Segments {
				if segment.SegmentIndex > index {
					invalidated[segment.SegmentIndex] = true
				}
			}
		}
	}
	issues, invalidAudio := timingAudioIssues(snapshot, before, after, sources, invalidated)
	for _, window := range after {
		if window.RequiredSpeechMs > window.DurationMs {
			issues = append(issues, DramaTimingIssue{"dialogue_exceeds_shot_window", window.SegmentIndex, window.ID, "", "按作品语速估算的对白与气口超出镜头时长；需核实际音频，不能标为口型或听审通过"})
		}
		if len(window.SourceShotIDs) > 1 {
			issues = append(issues, DramaTimingIssue{"merged_performance_requires_review", window.SegmentIndex, window.ID, "", "合并保留全部原句与来源镜号，需人工整理单次连续表演、机位和参考图；未生成新媒体"})
		}
	}
	casts := map[string]model.DramaCast{}
	for _, cast := range snapshot.Casts {
		casts[cast.ID] = cast
	}
	drafts := map[int]dramaSegmentDraft{}
	for _, draft := range buildDramaSegmentDrafts(settings, parseDramaBibleMeta(snapshot.Production.BibleJSON), shots, casts, snapshot.Segments) {
		drafts[draft.view.Index] = draft
	}
	previews := []map[string]any{}
	var total int64
	complete := true
	for i, segment := range snapshot.Segments {
		if !invalidated[segment.SegmentIndex] {
			continue
		}
		preview := map[string]any{"segmentIndex": segment.SegmentIndex, "segmentId": segment.ID, "directlyEdited": direct[segment.SegmentIndex],
			"expectedSegmentRevision": segment.Revision, "videoResourceId": segment.VideoResourceID, "videoStatus": segment.VideoStatus,
			"quoteStatus": "unavailable", "estimatedMicrocredits": nil, "quote": nil, "requiresPromptReview": true}
		draft, exists := drafts[segment.SegmentIndex]
		if !exists {
			preview["blockedReason"] = "合并使原段为空，需明确重新分段与首尾帧方案后再报价"
			issues = append(issues, DramaTimingIssue{"segment_would_be_empty", segment.SegmentIndex, "", "", preview["blockedReason"].(string)})
			complete = false
			previews = append(previews, preview)
			continue
		}
		var previous *model.DramaSegment
		if i > 0 {
			previous = &snapshot.Segments[i-1]
		}
		request, images, audios, prepareErr := s.prepareDramaSegment(settings, draft, segment, previous, casts)
		preview["durationMs"], preview["requestedSeconds"], preview["maxVideoSeconds"] = draft.view.DurationMs, request.Seconds, request.MaxVideoSeconds
		if prepareErr != nil || request.MaxVideoSeconds <= 0 || draft.view.DurationMs > int64(request.MaxVideoSeconds)*1000 {
			reason := "预演段时长超过模型能力，或没有可靠的模型时长合同"
			if prepareErr != nil {
				reason = prepareErr.Error()
			}
			preview["blockedReason"] = reason
			issues = append(issues, DramaTimingIssue{"segment_generation_blocked", segment.SegmentIndex, "", "", reason})
			complete = false
			previews = append(previews, preview)
			continue
		}
		config := dramaSegmentGenerationConfig(request)
		intent := ModelRequestIntentFromTaskInput(map[string]any{"mode": "video", "config": config, "referenceImages": images, "referenceAudios": audios}, "canvas_video", request.Operation)
		choice := segmentVideoChoice(segment, settings.Models.Video)
		var quote *LogicalModelQuote
		var quoteErr error
		if choice.LogicalModelID != "" {
			quote, quoteErr = s.QuoteLogicalModel(userID, choice.LogicalModelID, intent)
		} else {
			quote, quoteErr = s.QuoteChannelModel(userID, ChannelModelQuoteRequest{ChannelID: choice.ChannelID, ModelKey: choice.ModelKey, Intent: intent})
		}
		if quoteErr != nil || quote == nil {
			complete = false
			preview["blockedReason"] = "当前规格不可报价，不能当作免费"
			if quoteErr != nil {
				preview["blockedReason"] = quoteErr.Error()
			}
		} else {
			preview["quoteStatus"], preview["estimatedMicrocredits"], preview["quote"] = "estimated", quote.AmountMicrocredits, quote
			total += quote.AmountMicrocredits
		}
		previews = append(previews, preview)
	}
	var amount any
	if complete {
		amount = total
	}
	return map[string]any{"dryRun": true, "readOnly": true, "applied": false, "generationSubmitted": false, "canApply": false,
		"episodeRevision": episodeRevision, "timingRevision": timingRevision, "proposedEpisodeRevision": dramaShotRevision(shots, -1),
		"before": before, "after": after, "directlyEditedSegmentIndexes": sortedTimingIndexes(direct), "affectedSegmentIndexes": sortedTimingIndexes(invalidated),
		"audioVersionsRequiringRecheck": invalidAudio, "segmentGenerationPreviews": previews, "issues": issues,
		"savedAudioInspection": timingAudioInspection(snapshot), "asrStatusForProposedTiming": "unverified_requires_new_inspection_and_listening",
		"estimatedGenerationMicrocredits": amount, "estimateComplete": complete, "quoteIsAuthorization": false,
		"composeWouldBecomeStale": len(direct) > 0, "historicalMediaPreserved": true,
		"note": "仅预演；跨段合并保留首镜 ID 与段，列出后续失效范围，不修改正式分镜、音轨、审核、任务或账务。所有时间窗来自剧本或已保存音轨计划，不代表可听审或口型验收。"}, nil
}
