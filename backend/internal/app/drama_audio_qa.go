package app

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"yingce/backend/internal/model"
	"yingce/backend/internal/production"
)

type DramaAudioCheckView struct {
	model.DramaAudioCheck
	Result any `json:"result"`
}

type DramaAudioQARequest struct {
	DramaAudioScope
	AudioRevision   int64  `json:"audioRevision"`
	CueID           string `json:"cueId"`
	Layer           string `json:"layer" jsonschema:"content/picture/speaker/mouth/sound_effect/final"`
	Status          string `json:"status" jsonschema:"approved/rejected/unknown"`
	Reason          string `json:"reason"`
	ObservedStartMs int64  `json:"observedStartMs"`
	ObservedEndMs   int64  `json:"observedEndMs"`
	MouthState      string `json:"mouthState" jsonschema:"closed/speaking/not_visible/unknown"`
	ObservedSpeaker string `json:"observedSpeaker,omitempty" jsonschema:"speaker 层通过时须填写实际听审识别的人物名，与预期声源一致"`
	LipSyncChecked  bool   `json:"lipSyncChecked"`
	ExpectedCheckID string `json:"expectedCheckId"`
	IdempotencyKey  string `json:"idempotencyKey"`
}

func latestDramaAudioChecks(rows []model.DramaAudioCheck) map[string]model.DramaAudioCheck {
	latest := map[string]model.DramaAudioCheck{}
	for _, row := range rows {
		key := row.CueID + ":" + row.Layer
		if _, exists := latest[key]; !exists {
			latest[key] = row
		}
	}
	return latest
}

func dramaAudioCoverage(target dramaAudioTarget, cues []DramaAudioCue) []string {
	cues = append([]DramaAudioCue{}, cues...)
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].StartMs < cues[j].StartMs })
	texts := map[string]string{}
	for _, cue := range cues {
		if cue.Kind == "speech" {
			texts[cue.ShotID] += cue.Text
		}
	}
	issues := []string{}
	for _, window := range target.windows {
		if window.DialogueMode != "silent" && strings.TrimSpace(window.Text) != "" && production.NormalizeSpeechText(texts[window.ShotID]) != production.NormalizeSpeechText(window.Text) {
			issues = append(issues, window.ShotID+": 音轨文本未完整匹配现行分镜")
		}
	}
	return issues
}

func (s *Service) dramaAudioQA(version *DramaAudioVersionView, target dramaAudioTarget) (map[string]any, error) {
	result := map[string]any{"status": "unreviewed", "accepted": false, "checks": []DramaAudioCheckView{}, "cueResults": []map[string]any{}, "automaticMouthCheckAvailable": false}
	if version == nil {
		return result, nil
	}
	rows, err := s.repo.DramaAudioChecks(version.ProjectID, version.ID)
	if err != nil {
		return nil, err
	}
	checks := []DramaAudioCheckView{}
	for _, row := range rows {
		var value any
		if err := json.Unmarshal([]byte(row.ResultJSON), &value); err != nil {
			return nil, err
		}
		checks = append(checks, DramaAudioCheckView{DramaAudioCheck: row, Result: value})
	}
	result["checks"] = checks
	latest := latestDramaAudioChecks(rows)
	ready := len(version.Tracks) > 0 && len(dramaAudioCoverage(target, version.Tracks)) == 0 && version.VideoResourceID != ""
	rejected := false
	prerequisiteRejected := false
	for _, check := range latest {
		if check.Status == "rejected" || check.Status == "mismatch" {
			rejected = true
			if check.Layer != "final" {
				prerequisiteRejected = true
			}
		}
	}
	cueResults := []map[string]any{}
	for _, cue := range version.Tracks {
		layers := []string{"sound_effect"}
		if cue.Kind == "speech" {
			layers = []string{"asr_cue", "asr_video", "speaker", "mouth"}
		}
		complete := cue.ResourceID != ""
		if cue.Source == "native_video" {
			complete = version.VideoResourceID != ""
			layers = []string{"asr_video", "mouth"}
			if cue.Kind == "speech" {
				layers = append(layers, "speaker")
			}
		}
		states := map[string]string{}
		for _, layer := range layers {
			status := latest[cue.ID+":"+layer].Status
			if status == "" {
				status = "unreviewed"
			}
			states[layer] = status
			if status != "matched" && status != "approved" {
				complete = false
			}
		}
		ready = ready && complete
		cueResults = append(cueResults, map[string]any{"cueId": cue.ID, "checks": states, "complete": complete})
	}
	for _, layer := range []string{"content", "picture"} {
		status := firstNonEmpty(latest[":"+layer].Status, "unreviewed")
		result[layer+"Status"] = status
		ready = ready && status == "approved"
	}
	result["cueResults"], result["readyForFinalReview"] = cueResults, ready && !prerequisiteRejected
	result["finalStatus"] = firstNonEmpty(latest[":final"].Status, "unreviewed")
	finalCurrent := true
	if latest[":final"].Status == "approved" {
		var proof struct {
			CheckIDs map[string]string `json:"checkIds"`
		}
		if json.Unmarshal([]byte(latest[":final"].ResultJSON), &proof) != nil || len(proof.CheckIDs) == 0 {
			finalCurrent = false
		}
		for key, check := range latest {
			if check.Layer != "final" && proof.CheckIDs[key] != check.ID {
				finalCurrent = false
			}
		}
		if !finalCurrent {
			result["finalStatus"] = "stale"
		}
	}
	if rejected {
		result["status"] = "rejected"
	} else if ready && finalCurrent && latest[":final"].Status == "approved" {
		result["status"], result["accepted"] = "approved", true
	}
	if version.ShotRevision != dramaShotRevision(target.shots, target.segment.SegmentIndex) {
		result["status"], result["accepted"], result["readyForFinalReview"] = "stale", false, false
	}
	return result, nil
}

func (s *Service) loadDramaAudioCheckTarget(userID string, scope DramaAudioScope, revision int64, cueID string) (dramaAudioTarget, *DramaAudioVersionView, *DramaAudioCue, error) {
	target, err := s.dramaAudioTarget(userID, scope)
	if err != nil {
		return target, nil, nil, err
	}
	if revision <= 0 {
		return target, nil, nil, BadAuthRequest("检查须指定明确的 audioRevision")
	}
	version, _, err := s.dramaAudioVersion(scope.DramaID, target.segment.ID, revision)
	if err != nil {
		return target, nil, nil, err
	}
	if version == nil {
		return target, nil, nil, NotFound("音轨版本不存在")
	}
	for i := range version.Tracks {
		if version.Tracks[i].ID == cueID {
			return target, version, &version.Tracks[i], nil
		}
	}
	if cueID != "" {
		return target, nil, nil, NotFound("音轨片段不存在")
	}
	return target, version, nil, nil
}

func (s *Service) appendDramaAudioCheck(userID string, version *DramaAudioVersionView, cueID, layer, status, method, resourceID, sha string, value any, expectedID string) (any, error) {
	rows, err := s.repo.DramaAudioChecks(version.ProjectID, version.ID)
	if err != nil {
		return nil, err
	}
	if len(rows) >= 10000 {
		return nil, BadAuthRequest("该版本检查记录已达上限，请保存新音轨版本")
	}
	latest := latestDramaAudioChecks(rows)
	if latest[cueID+":"+layer].ID != expectedID {
		return nil, dramaMCPConflict(map[string]any{"checkId": latest[cueID+":"+layer].ID})
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	row := model.DramaAudioCheck{ID: newID(), ProjectID: version.ProjectID, EpisodeID: version.EpisodeID, SegmentID: version.SegmentID, VersionID: version.ID, CueID: cueID, Layer: layer, Status: status, Method: method, ResourceID: resourceID, ResourceSHA256: sha, ResultJSON: string(data), ReviewerID: userID, CreatedAt: time.Now()}
	row.Sequence = int64(len(rows) + 1)
	if err := s.repo.CreateDramaAudioCheck(&row); err != nil {
		return nil, err
	}
	return DramaAudioCheckView{DramaAudioCheck: row, Result: value}, nil
}
