package app

import (
	"strings"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

func applyDramaMCPShotReferences(shot *model.DramaShot, edit DramaShotEdit, known map[string]model.DramaCast) error {
	if edit.Scene != nil {
		if utf8.RuneCountInString(*edit.Scene) > 120 {
			return BadAuthRequest("scene 最多120字")
		}
		shot.Scene = strings.TrimSpace(*edit.Scene)
	}
	if edit.SceneCastID == nil && edit.CastIDs == nil && edit.PropIDs == nil && edit.LookCastIDs == nil {
		return nil
	}
	if edit.SceneCastID != nil {
		shot.SceneCastID = strings.TrimSpace(*edit.SceneCastID)
	}
	if shot.SceneCastID != "" {
		scene, ok := known[shot.SceneCastID]
		if !ok || scene.Kind != "scene" {
			return BadAuthRequest("sceneCastId 必须是当前作品的场景设定")
		}
		if edit.SceneCastID != nil && edit.Scene == nil {
			shot.Scene = scene.Name
		}
	}
	for _, field := range []struct {
		value  *[]string
		target *string
	}{{edit.CastIDs, &shot.CastIDsJSON}, {edit.PropIDs, &shot.PropIDsJSON}, {edit.LookCastIDs, &shot.LookCastIDsJSON}} {
		if field.value != nil {
			if len(*field.value) > dramaMaxReferenceLimit {
				return BadAuthRequest("镜头引用数量超限")
			}
			ids := []string{}
			for _, id := range *field.value {
				id = strings.TrimSpace(id)
				if id == "" || containsString(ids, id) {
					return BadAuthRequest("镜头引用 ID 不可为空或重复")
				}
				ids = append(ids, id)
			}
			*field.target = encodeStringList(ids)
		}
	}
	characters := decodeStringList(shot.CastIDsJSON)
	for _, id := range characters {
		row, ok := known[id]
		if !ok || row.Kind != "character" || row.ParentID != "" {
			return BadAuthRequest("castIds 必须是当前作品的角色本体，造型请用 lookCastIds")
		}
	}
	for _, id := range decodeStringList(shot.PropIDsJSON) {
		if row, ok := known[id]; !ok || row.Kind != "prop" {
			return BadAuthRequest("propIds 必须是当前作品的道具设定")
		}
	}
	parents := map[string]bool{}
	for _, id := range decodeStringList(shot.LookCastIDsJSON) {
		row, ok := known[id]
		if !ok || row.Kind != "character" || row.ParentID == "" || !containsString(characters, row.ParentID) || parents[row.ParentID] {
			return BadAuthRequest("lookCastIds 须属于镜头角色且每角色只选一套造型，请同步更新角色与造型")
		}
		parents[row.ParentID] = true
	}
	return nil
}

type DramaMCPReferenceRequest struct {
	DramaID                 string                       `json:"dramaId"`
	EpisodeID               string                       `json:"episodeId"`
	SegmentIndex            int                          `json:"segmentIndex"`
	ExpectedSegmentRevision int64                        `json:"expectedSegmentRevision"`
	ExpectedShotRevision    string                       `json:"expectedShotRevision"`
	IdempotencyKey          string                       `json:"idempotencyKey"`
	ReferenceCastIDs        *[]string                    `json:"referenceCastIds,omitempty"`
	AutoReferences          bool                         `json:"autoReferences"`
	ReferenceImages         *[]model.DramaReferenceImage `json:"referenceImages,omitempty"`
}

func (s *Service) EditDramaSegmentReferencesMCP(userID string, req DramaMCPReferenceRequest) (map[string]any, error) {
	return s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "segment_references", req.IdempotencyKey, req, func(local *Service) (any, error) {
		rows, err := local.repo.DramaSegments(req.EpisodeID)
		if err != nil {
			return nil, err
		}
		var target *model.DramaSegment
		for i := range rows {
			if rows[i].SegmentIndex == req.SegmentIndex {
				target = &rows[i]
				break
			}
		}
		if target == nil {
			return nil, NotFound("剧情段不存在")
		}
		shots, err := local.repo.DramaShots(req.EpisodeID)
		if err != nil {
			return nil, err
		}
		shotRevision := dramaShotRevision(shots, req.SegmentIndex)
		if target.Revision != req.ExpectedSegmentRevision || req.ExpectedShotRevision == "" || shotRevision != req.ExpectedShotRevision {
			return nil, local.dramaGenerationConflict(req.EpisodeID, []int{req.SegmentIndex})
		}
		if req.AutoReferences && req.ReferenceCastIDs != nil {
			return nil, BadAuthRequest("autoReferences 与 referenceCastIds 不可同时指定")
		}
		if err := local.UpdateDramaSegment(userID, req.DramaID, req.EpisodeID, req.SegmentIndex, UpdateDramaSegmentRequest{ReferenceCastIDs: req.ReferenceCastIDs, AutoReferences: req.AutoReferences, ReferenceImages: req.ReferenceImages}); err != nil {
			return nil, err
		}
		after, err := local.repo.DramaSegment(req.DramaID, target.ID)
		if err != nil {
			return nil, err
		}
		if after.Revision != target.Revision {
			if err := local.repo.UpdateDramaSegmentFields(after.ID, map[string]any{"prompt_status": model.DramaPromptConflict, "candidate_id": "", "candidate_shot_revision": ""}); err != nil {
				return nil, err
			}
			after.PromptStatus = model.DramaPromptConflict
		}
		return map[string]any{"segmentIndex": req.SegmentIndex, "segmentRevision": after.Revision, "shotRevision": shotRevision, "promptStatus": after.PromptStatus, "referencesChanged": after.Revision != target.Revision, "action": "references_saved_prompts_require_review"}, nil
	})
}
