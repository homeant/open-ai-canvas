package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

type ExternalPrevisScope struct {
	DramaID   string `json:"dramaId,omitempty"`
	EpisodeID string `json:"episodeId,omitempty"`
	CanvasID  string `json:"canvasId,omitempty"`
}

type ExternalPrevisReadRequest struct {
	DramaID           string   `json:"dramaId,omitempty"`
	EpisodeID         string   `json:"episodeId,omitempty"`
	CanvasID          string   `json:"canvasId,omitempty"`
	SceneID           string   `json:"sceneId,omitempty"`
	NodeID            string   `json:"nodeId,omitempty"`
	ShotID            string   `json:"shotId,omitempty"`
	ObjectIDs         []string `json:"objectIds,omitempty"`
	IncludeTransforms bool     `json:"includeTransforms,omitempty"`
}

type ExternalPrevisCreateRequest struct {
	DramaID            string `json:"dramaId"`
	EpisodeID          string `json:"episodeId"`
	CanvasID           string `json:"canvasId,omitempty"`
	CanvasSnapshotHash string `json:"canvasSnapshotHash"`
	IdempotencyKey     string `json:"idempotencyKey"`
	Title              string `json:"title"`
	TemplateID         string `json:"templateId"`
	DryRun             bool   `json:"dryRun,omitempty"`
}

type ExternalPrevisPatchOperation = cloudAgentPrevisPatchOperation

type ExternalPrevisPatchRequest struct {
	DramaID          string                         `json:"dramaId,omitempty"`
	EpisodeID        string                         `json:"episodeId,omitempty"`
	CanvasID         string                         `json:"canvasId,omitempty"`
	SceneID          string                         `json:"sceneId"`
	SnapshotHash     string                         `json:"snapshotHash"`
	ExpectedRevision *int64                         `json:"expectedRevision,omitempty"`
	IdempotencyKey   string                         `json:"idempotencyKey"`
	Operations       []ExternalPrevisPatchOperation `json:"operations"`
	DryRun           bool                           `json:"dryRun,omitempty"`
}

type ExternalPrevisPreviewRequest struct {
	DramaID   string   `json:"dramaId"`
	EpisodeID string   `json:"episodeId"`
	CanvasID  string   `json:"canvasId,omitempty"`
	SceneID   string   `json:"sceneId"`
	ShotID    string   `json:"shotId"`
	Duration  *float64 `json:"duration,omitempty"`
	FPS       *int     `json:"fps,omitempty"`
}

func (s *Service) externalPrevisCanvases(userID string, scope ExternalPrevisScope) ([]model.CanvasProject, error) {
	if scope.CanvasID != "" && scope.DramaID == "" {
		canvas, err := s.repo.CanvasProjectForUser(userID, scope.CanvasID)
		if err != nil {
			return nil, dramaLookupError(err)
		}
		if scope.EpisodeID != "" {
			if _, err := s.repo.DramaEpisode(canvas.ProjectID, scope.EpisodeID); err != nil {
				return nil, dramaLookupError(err)
			}
		}
		return []model.CanvasProject{*canvas}, nil
	}
	if scope.DramaID == "" || scope.EpisodeID == "" {
		return nil, BadAuthRequest("预演台操作需要 dramaId 和 episodeId")
	}
	project, _, err := s.dramaForUser(userID, scope.DramaID)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.DramaEpisode(project.ID, scope.EpisodeID); err != nil {
		return nil, dramaLookupError(err)
	}
	if scope.CanvasID != "" {
		canvas, err := s.repo.CanvasProjectForUser(userID, scope.CanvasID)
		if err != nil {
			return nil, dramaLookupError(err)
		}
		if canvas.ProjectID != project.ID {
			return nil, Forbidden("画布不属于指定作品")
		}
		return []model.CanvasProject{*canvas}, nil
	}
	return s.repo.ProjectCanvasSummaries(userID, project.ID)
}

func (s *Service) externalPrevisCanvas(userID string, scope ExternalPrevisScope) (*model.CanvasProject, error) {
	canvases, err := s.externalPrevisCanvases(userID, scope)
	if err != nil {
		return nil, err
	}
	if len(canvases) != 1 {
		return nil, BadAuthRequest("当前作品没有唯一画布；请先调用 list_previs_scenes 并指定 canvasId")
	}
	return &canvases[0], nil
}

func externalPrevisCall(name string, args any) (cloudAgentCall, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return cloudAgentCall{}, err
	}
	call := cloudAgentCall{}
	call.Function.Name = name
	call.Function.Arguments = string(raw)
	return call, nil
}

func (s *Service) ListExternalPrevisScenes(userID string, scope ExternalPrevisScope) (map[string]any, error) {
	canvases, err := s.externalPrevisCanvases(userID, scope)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0)
	canvasItems := make([]map[string]any, 0, len(canvases))
	for _, canvas := range canvases {
		full, err := s.repo.CanvasProjectForUser(userID, canvas.ID)
		if err != nil {
			return nil, err
		}
		doc, err := creationDocument(full.PayloadJSON)
		if err != nil {
			return nil, err
		}
		nodes := creationMaps(doc["nodes"])
		canvasItems = append(canvasItems, map[string]any{"canvasId": canvas.ID, "title": canvas.Title, "revision": full.Revision, "canvasSnapshotHash": cloudAgentCanvasHash(doc)})
		for _, scene := range creationMaps(doc["previsScenes"]) {
			bound := stringValue(scene["episodeId"])
			if bound != "" && scope.EpisodeID != "" && bound != scope.EpisodeID {
				continue
			}
			sceneID := stringValue(scene["id"])
			objectSummaries := make([]map[string]any, 0)
			for _, object := range creationMaps(scene["objects"]) {
				if len(objectSummaries) == 16 {
					break
				}
				objectSummaries = append(objectSummaries, map[string]any{"id": stringValue(object["id"]), "name": truncateRunes(stringValue(object["name"]), 160), "kind": stringValue(object["kind"])})
			}
			shotSummaries := make([]map[string]any, 0)
			for _, shot := range creationMaps(scene["shots"]) {
				shotSummaries = append(shotSummaries, map[string]any{"id": stringValue(shot["id"]), "name": truncateRunes(stringValue(shot["name"]), 160), "cameraId": stringValue(shot["cameraId"])})
			}
			cameraSummaries := make([]map[string]any, 0)
			for _, camera := range creationMaps(scene["cameras"]) {
				cameraSummaries = append(cameraSummaries, map[string]any{"id": stringValue(camera["id"]), "name": truncateRunes(stringValue(camera["name"]), 160)})
			}
			nodeID := cloudAgentPrevisBoundNodeID(nodes, sceneID)
			items = append(items, map[string]any{"canvasId": canvas.ID, "canvasTitle": canvas.Title, "canvasRevision": full.Revision, "canvasSnapshotHash": cloudAgentCanvasHash(doc), "episodeId": bound, "sceneId": sceneID, "nodeId": nodeID, "title": truncateRunes(stringValue(scene["title"]), 160), "snapshotHash": creationHash(scene), "activeShotId": stringValue(scene["activeShotId"]), "objectCount": len(creationMaps(scene["objects"])), "objectSummary": objectSummaries, "shotCount": len(shotSummaries), "shots": shotSummaries, "cameras": cameraSummaries})
		}
	}
	return map[string]any{"dramaId": scope.DramaID, "episodeId": scope.EpisodeID, "canvases": canvasItems, "scenes": items, "canvasCount": len(canvases)}, nil
}

func (s *Service) GetExternalPrevisScene(userID string, req ExternalPrevisReadRequest) (map[string]any, error) {
	canvas, err := s.externalPrevisCanvas(userID, ExternalPrevisScope{req.DramaID, req.EpisodeID, req.CanvasID})
	if err != nil {
		return nil, err
	}
	if req.NodeID != "" {
		doc, err := creationDocument(canvas.PayloadJSON)
		if err != nil {
			return nil, err
		}
		var sceneID string
		for _, node := range creationMaps(doc["nodes"]) {
			if stringValue(node["id"]) == req.NodeID {
				meta, _ := node["metadata"].(map[string]any)
				sceneID = stringValue(meta["previsSceneId"])
			}
		}
		if sceneID == "" || (req.SceneID != "" && sceneID != req.SceneID) {
			return nil, BadAuthRequest("预演节点不存在或与 sceneId 不匹配")
		}
		req.SceneID = sceneID
	}
	call, err := externalPrevisCall("previs_scene_read", map[string]any{"sceneId": req.SceneID, "episodeId": req.EpisodeID, "shotId": req.ShotID, "objectIds": req.ObjectIDs, "includeTransforms": req.IncludeTransforms})
	if err != nil {
		return nil, err
	}
	result, err := cloudAgentPrevisSceneRead(s.repo, userID, canvas.ID, call)
	if err != nil {
		return nil, err
	}
	view := result.(map[string]any)
	view["dramaId"] = req.DramaID
	view["episodeId"] = req.EpisodeID
	return view, nil
}

func (s *Service) ExternalPrevisCapabilities() map[string]any {
	capabilities := map[string]any{"coordinateSystem": "Y up; meters; position=[X,Y,Z]; rotation radians", "maxScenesPerCanvas": cloudAgentPrevisMaxScenes, "maxOperationsPerPatch": cloudAgentPrevisMaxOperations, "maxObjectsPerScene": cloudAgentPrevisMaxObjects, "maxShotsPerScene": cloudAgentPrevisMaxShots, "templates": []string{"empty", "monologue", "dialogue", "blocking", "product", "action_chase", "interior", "crowd"}, "operationSchema": cloudAgentPrevisPatchOperationSchema(), "characterBinding": "actor object_add/object_update may use characterAssetId + characterVersionId + referenceNodeId only when the matching character card is present in the same canvas", "modelImport": "upload_previs_model or import_resource, then inspect_previs_model and object_add kind=model", "render": "render_previs_frame and render_previs_video use the production PrevisViewport in headless Chromium; upload_previs_render and publish_previs_render remain available for external outputs", "maxMCPUploadBytes": externalPrevisMCPModelLimit, "maxRenderVideoSeconds": 10}
	capabilities["motionValidation"] = map[string]any{"frameSampling": "sample_previs_frames", "maxFramesPerSample": 8, "sampleReadOnly": true, "perShotBoneTracks": "UI", "ikConstraintsSupported": false, "preciseCollisionDetection": false, "broadPhaseOverlapWarnings": true}
	return capabilities
}

func externalPrevisDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func externalPrevisKey(value string) error {
	if strings.TrimSpace(value) != value || len(value) < 8 || len(value) > 120 {
		return BadAuthRequest("idempotencyKey 长度须为 8–120 字节且不能含首尾空白")
	}
	return nil
}

func (s *Service) externalPrevisWrite(userID, canvasID, key, digest string, call cloudAgentCall, dryRun bool, expectedRevision ...*int64) (map[string]any, error) {
	if dryRun {
		var plan *cloudAgentPrevisMutationPlan
		var err error
		if call.Function.Name == "previs_scene_create" {
			plan, err = prepareCloudAgentPrevisSceneCreate(s.repo, userID, canvasID, call)
		} else {
			plan, err = prepareCloudAgentPrevisApplyPatch(s.repo, userID, canvasID, call)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"committed": false, "sceneId": plan.SceneID, "nodeId": plan.NodeID, "shotId": plan.ShotID, "snapshotHash": plan.ResultSnapshotHash, "canvasSnapshotHash": plan.AfterSnapshotHash, "preview": plan.Preview}, nil
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		return nil, err
	}
	var result map[string]any
	mutationID := "mcp-previs:" + externalPrevisDigest(key)
	err = s.repo.Transaction(func(tx *repository.Repository) error {
		if err := tx.LockCanvasMutation(userID, canvasID); err != nil {
			return err
		}
		receipt := &model.CanvasMutationReceipt{ID: newID(), UserID: userID, CanvasID: canvasID, MutationID: mutationID, PayloadDigest: digest}
		created, err := tx.TryCreateCanvasMutationReceipt(receipt)
		if err != nil {
			return err
		}
		if !created {
			previous, err := tx.CanvasMutationReceiptForUser(userID, canvasID, mutationID)
			if err != nil {
				return err
			}
			if previous.PayloadDigest != digest {
				return creationConflict("同一幂等键对应的预演操作内容不同")
			}
			if previous.ResultJSON == "" {
				return creationConflict("预演操作仍在进行，请稍后重试")
			}
			return json.Unmarshal([]byte(previous.ResultJSON), &result)
		}
		if len(expectedRevision) > 0 && expectedRevision[0] != nil {
			current, err := tx.CanvasProjectForUser(userID, canvasID)
			if err != nil {
				return err
			}
			if current.Revision != *expectedRevision[0] {
				return creationConflict("画布 revision 已变化，请重新读取预演场景")
			}
		}
		value, err := applyCloudAgentPrevisMutation(tx, userID, canvasID, call, policy)
		if err != nil {
			return err
		}
		result = value.(map[string]any)
		canvas, err := tx.CanvasProjectForUser(userID, canvasID)
		if err != nil {
			return err
		}
		result["revision"] = canvas.Revision
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		receipt.ResultJSON = string(raw)
		receipt.Revision = canvas.Revision
		return tx.FinalizeCanvasMutationReceipt(receipt)
	})
	return result, err
}

func (s *Service) CreateExternalPrevisScene(userID string, req ExternalPrevisCreateRequest) (map[string]any, error) {
	if err := externalPrevisKey(req.IdempotencyKey); err != nil {
		return nil, err
	}
	canvas, err := s.externalPrevisCanvas(userID, ExternalPrevisScope{req.DramaID, req.EpisodeID, req.CanvasID})
	if err != nil {
		return nil, err
	}
	sceneID := "mcp-" + externalPrevisDigest([]string{userID, canvas.ID, req.IdempotencyKey})[:32]
	args := cloudAgentPrevisSceneCreateArgs{CanvasSnapshotHash: req.CanvasSnapshotHash, SceneID: sceneID, EpisodeID: req.EpisodeID, Title: req.Title, TemplateID: req.TemplateID}
	call, err := externalPrevisCall("previs_scene_create", args)
	if err != nil {
		return nil, err
	}
	result, err := s.externalPrevisWrite(userID, canvas.ID, req.IdempotencyKey, externalPrevisDigest([]any{"create", args}), call, req.DryRun)
	if err != nil {
		return nil, err
	}
	result["episodeId"] = req.EpisodeID
	result["dramaId"] = req.DramaID
	return result, nil
}

func (s *Service) ApplyExternalPrevisPatch(userID string, req ExternalPrevisPatchRequest) (map[string]any, error) {
	if err := externalPrevisKey(req.IdempotencyKey); err != nil {
		return nil, err
	}
	for _, operation := range req.Operations {
		if operation.ResourceID == nil {
			continue
		}
		model, err := s.ExternalPrevisModelNodes(userID, *operation.ResourceID)
		if err != nil {
			return nil, err
		}
		if operation.GLTFNodeIndex != nil {
			found := false
			for _, node := range model["nodes"].([]map[string]any) {
				if node["index"] == *operation.GLTFNodeIndex {
					found = true
					break
				}
			}
			if !found {
				return nil, BadAuthRequest("gltfNodeIndex 不对应可见 mesh 节点")
			}
		}
	}
	canvas, err := s.externalPrevisCanvas(userID, ExternalPrevisScope{req.DramaID, req.EpisodeID, req.CanvasID})
	if err != nil {
		return nil, err
	}
	args := cloudAgentPrevisApplyPatchArgs{SnapshotHash: req.SnapshotHash, SceneID: req.SceneID, EpisodeID: req.EpisodeID, Operations: req.Operations}
	call, err := externalPrevisCall("previs_apply_patch", args)
	if err != nil {
		return nil, err
	}
	if req.DryRun && req.ExpectedRevision != nil && canvas.Revision != *req.ExpectedRevision {
		return nil, creationConflict("画布 revision 已变化，请重新读取预演场景")
	}
	result, err := s.externalPrevisWrite(userID, canvas.ID, req.IdempotencyKey, externalPrevisDigest([]any{"patch", req.EpisodeID, args, req.ExpectedRevision}), call, req.DryRun, req.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	result["episodeId"] = req.EpisodeID
	result["dramaId"] = req.DramaID
	operationResults := make([]map[string]any, 0, len(req.Operations))
	status := "applied"
	if req.DryRun {
		status = "planned"
	}
	for _, operation := range req.Operations {
		operationResults = append(operationResults, map[string]any{"type": operation.Type, "id": operation.ID, "status": status})
	}
	result["operationResults"] = operationResults
	return result, nil
}

func (s *Service) ExternalPrevisPreviewPlan(userID string, req ExternalPrevisPreviewRequest) (map[string]any, error) {
	canvas, err := s.externalPrevisCanvas(userID, ExternalPrevisScope{req.DramaID, req.EpisodeID, req.CanvasID})
	if err != nil {
		return nil, err
	}
	call, err := externalPrevisCall("previs_preview", map[string]any{"sceneId": req.SceneID, "episodeId": req.EpisodeID, "shotId": req.ShotID, "duration": req.Duration, "fps": req.FPS, "output": "clay_video"})
	if err != nil {
		return nil, err
	}
	value, err := cloudAgentPrevisPreview(s.repo, userID, canvas.ID, call)
	if err != nil {
		return nil, err
	}
	result := value.(map[string]any)
	delete(result, "text")
	result["status"] = "ready_to_render"
	result["recordingStarted"] = false
	result["note"] = "调用 render_previs_frame 或 render_previs_video 即可在服务端生成并绑定资源；随后 get_previs_render 可回读"
	return result, nil
}
