package app

import (
	"encoding/json"
	"errors"

	"gorm.io/gorm"
)

type ExternalPrevisCanvasCreateRequest struct {
	DramaID        string `json:"dramaId"`
	EpisodeID      string `json:"episodeId"`
	Title          string `json:"title"`
	IdempotencyKey string `json:"idempotencyKey"`
}

func (s *Service) CreateExternalPrevisCanvas(userID string, req ExternalPrevisCanvasCreateRequest) (map[string]any, error) {
	if err := externalPrevisKey(req.IdempotencyKey); err != nil {
		return nil, err
	}
	if err := cloudAgentPrevisValidateText(req.Title, "画布标题", 160, true); err != nil {
		return nil, err
	}
	if _, err := s.externalPrevisCanvases(userID, ExternalPrevisScope{DramaID: req.DramaID, EpisodeID: req.EpisodeID}); err != nil {
		return nil, err
	}
	id := "mcp-canvas-" + externalPrevisDigest([]string{userID, req.DramaID, req.EpisodeID, req.IdempotencyKey})[:32]
	if existing, err := s.repo.CanvasProjectForUser(userID, id); err == nil {
		if existing.ProjectID != req.DramaID || existing.Title != req.Title {
			return nil, creationConflict("同一幂等键已用于其他画布")
		}
		return map[string]any{"canvasId": id, "title": existing.Title, "revision": existing.Revision, "created": false}, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"id": id, "projectId": req.DramaID, "title": req.Title, "revision": 0, "nodes": []any{}, "connections": []any{}, "previsScenes": []any{}})
	if err != nil {
		return nil, err
	}
	summary, err := s.UpsertUserCanvasProject(userID, raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"canvasId": id, "title": req.Title, "revision": summary.Revision, "created": true}, nil
}

func (s *Service) ListExternalPrevisReferences(userID string, scope ExternalPrevisScope) (map[string]any, error) {
	if _, err := s.externalPrevisCanvases(userID, scope); err != nil {
		return nil, err
	}
	casts, err := s.repo.DramaCasts(scope.DramaID)
	if err != nil {
		return nil, err
	}
	shots, err := s.repo.DramaShots(scope.EpisodeID)
	if err != nil {
		return nil, err
	}
	castItems := make([]map[string]any, 0, len(casts))
	for _, cast := range casts {
		castItems = append(castItems, map[string]any{"id": cast.ID, "name": cast.Name, "kind": cast.Kind, "parentId": cast.ParentID, "imageResourceId": cast.ImageResourceID, "role": cast.Role})
	}
	shotItems := make([]map[string]any, 0, len(shots))
	for _, shot := range shots {
		if shot.ProjectID == scope.DramaID {
			shotItems = append(shotItems, map[string]any{"id": shot.ID, "position": shot.Position, "scene": shot.Scene, "shotSize": shot.ShotSize, "camera": shot.Camera, "durationMs": shot.DurationMs, "segmentIndex": shot.SegmentIndex})
		}
	}
	return map[string]any{"casts": castItems, "shots": shotItems, "dramaId": scope.DramaID, "episodeId": scope.EpisodeID}, nil
}

func (s *Service) ValidateExternalPrevisScene(userID string, req ExternalPrevisReadRequest) (map[string]any, error) {
	canvas, err := s.externalPrevisCanvas(userID, ExternalPrevisScope{req.DramaID, req.EpisodeID, req.CanvasID})
	if err != nil {
		return nil, err
	}
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		return nil, err
	}
	scene, ok := findPrevisScene(creationMaps(doc["previsScenes"]), req.SceneID)
	if !ok || (req.EpisodeID != "" && stringValue(scene["episodeId"]) != "" && stringValue(scene["episodeId"]) != req.EpisodeID) {
		return nil, BadAuthRequest("预演场景不存在或属于其他剧集")
	}
	issues := make([]map[string]any, 0)
	if err := cloudAgentPrevisValidateScene(scene); err != nil {
		issues = append(issues, map[string]any{"code": "invalid_scene", "message": err.Error()})
	}
	shotIDs := map[string]bool{}
	for _, shot := range creationMaps(scene["shots"]) {
		if id := stringValue(shot["dramaShotId"]); id != "" {
			if shotIDs[id] {
				issues = append(issues, map[string]any{"code": "duplicate_drama_shot", "shotId": stringValue(shot["id"])})
			}
			shotIDs[id] = true
			linked, lookupErr := s.repo.DramaShot(canvas.ProjectID, id)
			if lookupErr != nil || (req.EpisodeID != "" && linked.EpisodeID != req.EpisodeID) {
				issues = append(issues, map[string]any{"code": "invalid_drama_shot", "shotId": stringValue(shot["id"])})
			}
		}
		if previsEffectiveAspectRatio(shot) != "9:16" {
			issues = append(issues, map[string]any{"code": "aspect_ratio_not_vertical", "shotId": stringValue(shot["id"])})
		}
	}
	modelChecks := map[string]bool{}
	for _, object := range creationMaps(scene["objects"]) {
		if id := stringValue(object["dramaCastId"]); id != "" {
			if _, lookupErr := s.repo.DramaCast(canvas.ProjectID, id); lookupErr != nil {
				issues = append(issues, map[string]any{"code": "invalid_drama_cast", "objectId": stringValue(object["id"])})
			}
		}
		if stringValue(object["kind"]) != "model" {
			continue
		}
		resourceID := stringValue(object["resourceId"])
		if resourceID == "" {
			issues = append(issues, map[string]any{"code": "model_missing_resource", "objectId": stringValue(object["id"])})
			continue
		}
		if checked, found := modelChecks[resourceID]; found {
			if !checked {
				issues = append(issues, map[string]any{"code": "model_unavailable", "objectId": stringValue(object["id"])})
			}
			continue
		}
		_, lookupErr := s.ExternalPrevisModelNodes(userID, resourceID)
		modelChecks[resourceID] = lookupErr == nil
		if lookupErr != nil {
			issues = append(issues, map[string]any{"code": "model_unavailable", "objectId": stringValue(object["id"])})
		}
	}
	warnings := previsAnimationWarnings(scene, req.ShotID)
	return map[string]any{"sceneId": req.SceneID, "snapshotHash": creationHash(scene), "valid": len(issues) == 0, "issues": issues, "validationScope": "structure_and_references", "motionValidated": false, "ikConstraintsSupported": false, "requiresVisualReview": true, "warnings": warnings}, nil
}
