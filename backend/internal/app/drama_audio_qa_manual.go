package app

import (
	"strings"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

func (s *Service) RecordDramaAudioQA(userID string, req DramaAudioQARequest) (map[string]any, error) {
	return s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "audio.qa", req.IdempotencyKey, req, func(local *Service) (any, error) {
		target, version, cue, err := local.loadDramaAudioCheckTarget(userID, req.DramaAudioScope, req.AudioRevision, req.CueID)
		if err != nil {
			return nil, err
		}
		if req.Status != "approved" && req.Status != "rejected" && req.Status != "unknown" {
			return nil, BadAuthRequest("QA 状态无效")
		}
		if strings.TrimSpace(req.Reason) == "" || utf8.RuneCountInString(req.Reason) > 2000 {
			return nil, BadAuthRequest("QA 须填写观察依据或拒绝原因（1–2000 字）")
		}
		if req.Status == "approved" && version.ShotRevision != dramaShotRevision(target.shots, req.SegmentIndex) {
			return nil, BadAuthRequest("分镜已变化，旧音轨版本不能验收通过")
		}
		start, end := int64(0), target.totalMs
		resourceID := version.VideoResourceID
		switch req.Layer {
		case "content", "picture", "final":
			if cue != nil {
				return nil, BadAuthRequest("内容、画面和最终验收须针对整版，cueId 留空")
			}
		case "mouth", "speaker", "sound_effect":
			if cue == nil || (req.Layer == "sound_effect") != (cue.Kind == "sfx") {
				return nil, BadAuthRequest("检查层与音轨片段类型不符")
			}
			if req.Layer == "speaker" && cue.Kind != "speech" {
				return nil, BadAuthRequest("人物声源检查仅适用于语音片段")
			}
			if utf8.RuneCountInString(req.ObservedSpeaker) > 60 {
				return nil, BadAuthRequest("实际声源名称过长")
			}
			if req.Layer == "speaker" && req.Status == "approved" && (strings.TrimSpace(req.ObservedSpeaker) == "" || req.ObservedSpeaker != cue.Speaker) {
				return nil, BadAuthRequest("人物声源通过须明确实际听审人物，且与预期声源一致")
			}
			start, end = cue.StartMs, cue.EndMs
			if req.Layer == "mouth" {
				if req.MouthState != "closed" && req.MouthState != "speaking" && req.MouthState != "not_visible" && req.MouthState != "unknown" {
					return nil, BadAuthRequest("须填写实际观察到的嘴部状态")
				}
				if req.Status == "approved" && ((cue.DialogueMode == "dialogue" && (req.MouthState != "speaking" || !req.LipSyncChecked)) || (cue.DialogueMode == "inner_monologue" && req.MouthState != "closed") || ((cue.DialogueMode == "voice_over" || cue.DialogueMode == "silent") && req.MouthState != "closed" && req.MouthState != "not_visible")) {
					return nil, BadAuthRequest("开口对白须检查嘴部同步；独白/旁白须确认闭口，不可见保持未验证")
				}
			}
		default:
			return nil, BadAuthRequest("QA 层无效；ASR 结果只能由服务端实测写入")
		}
		if req.ObservedStartMs != start || req.ObservedEndMs != end {
			return nil, BadAuthRequest("人工检查必须覆盖完整片段／版本时间窗")
		}
		if resourceID == "" && req.Layer != "content" {
			return nil, BadAuthRequest("请先绑定明确视频版本再检查音画实际产物")
		}
		if resourceID != "" {
			resource, err := local.repo.ResourceForUser(userID, resourceID)
			if err != nil || resource == nil || resource.Status != model.ResourceStatusReady {
				return nil, BadAuthRequest("视频资源未就绪或已删除")
			}
		}
		if req.Layer == "final" && req.Status == "approved" {
			qa, err := local.dramaAudioQA(version, target)
			if err != nil {
				return nil, err
			}
			if qa["readyForFinalReview"] != true {
				return nil, BadAuthRequest("内容、画面、ASR、声源、嘴部或音效仍有未验证／拒绝项")
			}
		}
		audit := req
		audit.IdempotencyKey = ""
		var proof any = audit
		if req.Layer == "final" {
			rows, err := local.repo.DramaAudioChecks(req.DramaID, version.ID)
			if err != nil {
				return nil, err
			}
			ids := map[string]string{}
			for key, check := range latestDramaAudioChecks(rows) {
				if check.Layer != "final" {
					ids[key] = check.ID
				}
			}
			proof = map[string]any{"review": audit, "checkIds": ids}
		}
		return local.appendDramaAudioCheck(userID, version, req.CueID, req.Layer, req.Status, "manual", resourceID, "", proof, req.ExpectedCheckID)
	})
}
