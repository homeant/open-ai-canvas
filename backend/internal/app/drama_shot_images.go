package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

type DramaShotImagesRequest struct {
	DramaID                 string                          `json:"dramaId"`
	EpisodeID               string                          `json:"episodeId"`
	ShotID                  string                          `json:"shotId"`
	ExpectedEpisodeRevision string                          `json:"expectedEpisodeRevision"`
	References              []model.DramaShotImageReference `json:"references"`
	DryRun                  bool                            `json:"dryRun"`
	IdempotencyKey          string                          `json:"idempotencyKey"`
}

type DramaCanvasImageCandidatesRequest struct {
	DramaID      string `json:"dramaId"`
	EpisodeID    string `json:"episodeId"`
	CanvasID     string `json:"canvasId"`
	Offset       int    `json:"offset,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	ResourceID   string `json:"resourceId,omitempty"`
	SnapshotHash string `json:"snapshotHash,omitempty"`
}

func (s *Service) resolveDramaShotImages(userID, projectID string, inputs []model.DramaShotImageReference) ([]model.DramaShotImageReference, error) {
	if len(inputs) > 4 {
		return nil, BadAuthRequest("每镜最多4张明确图片")
	}
	result := []model.DramaShotImageReference{}
	seen := map[string]bool{}
	for _, ref := range inputs {
		if ref.Role != "first_frame" && ref.Role != "information" {
			return nil, BadAuthRequest("图片角色须为 first_frame/information")
		}
		if ref.CanvasID != "" || ref.NodeID != "" || ref.NodeHash != "" {
			canvas, err := s.repo.CanvasProjectForUser(userID, ref.CanvasID)
			if err != nil {
				return nil, dramaLookupError(err)
			}
			if canvas.ProjectID != projectID {
				return nil, Forbidden("图片画布不属于当前作品")
			}
			doc, err := creationDocument(canvas.PayloadJSON)
			if err != nil {
				return nil, err
			}
			nodes, err := creationObjects(doc["nodes"])
			if err != nil {
				return nil, err
			}
			node := nodes[ref.NodeID]
			if node == nil || node["type"] != "image" {
				return nil, BadAuthRequest("请指定真实图片节点")
			}
			if ref.NodeHash == "" || cloudAgentNodeHash(doc, ref.NodeID) != ref.NodeHash {
				return nil, dramaMCPConflict(map[string]any{"nodeHash": cloudAgentNodeHash(doc, ref.NodeID)})
			}
			media, field, err := cloudAgentMediaReference(s.repo, userID, projectID, node)
			if err != nil {
				return nil, err
			}
			if field != "referenceImages" {
				return nil, BadAuthRequest("节点不是图片")
			}
			id := strings.TrimPrefix(stringValue(media["storageKey"]), "resource:")
			if ref.ResourceID != "" && ref.ResourceID != id {
				return nil, BadAuthRequest("resourceId 与真实节点不一致")
			}
			ref.ResourceID = id
		}
		resource, err := s.repo.ResourceForUser(userID, ref.ResourceID)
		if err != nil || resource == nil || resource.Kind != "image" || resource.Status != model.ResourceStatusReady {
			return nil, BadAuthRequest("关键图须为当前用户已就绪的图片")
		}
		if seen[ref.ResourceID] {
			return nil, BadAuthRequest("同镜图片重复")
		}
		seen[ref.ResourceID] = true
		if utf8.RuneCountInString(ref.Name) > 120 {
			return nil, BadAuthRequest("图片说明最多120字")
		}
		ref.Name = firstNonEmpty(strings.TrimSpace(ref.Name), "镜头实体信息图")
		result = append(result, ref)
	}
	return result, nil
}

func (s *Service) refreshDramaShotCanvasImage(userID, projectID string, ref model.DramaShotImageReference) (model.DramaShotImageReference, error) {
	if ref.CanvasID == "" || ref.NodeID == "" {
		return ref, BadAuthRequest("这张镜头图片没有可同步的画布节点")
	}
	canvas, err := s.repo.CanvasProjectForUser(userID, ref.CanvasID)
	if err != nil {
		return ref, dramaLookupError(err)
	}
	if canvas.ProjectID != projectID {
		return ref, Forbidden("图片画布不属于当前作品")
	}
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		return ref, err
	}
	nodes, err := creationObjects(doc["nodes"])
	if err != nil {
		return ref, err
	}
	node := nodes[ref.NodeID]
	if node == nil || node["type"] != "image" {
		return ref, BadAuthRequest("原画布节点已不存在或不再是图片，请重新绑定")
	}
	media, field, err := cloudAgentMediaReference(s.repo, userID, projectID, node)
	if err != nil {
		return ref, err
	}
	if field != "referenceImages" {
		return ref, BadAuthRequest("原画布节点不再是图片")
	}
	ref.ResourceID = strings.TrimPrefix(stringValue(media["storageKey"]), "resource:")
	ref.NodeHash = cloudAgentNodeHash(doc, ref.NodeID)
	resolved, err := s.resolveDramaShotImages(userID, projectID, []model.DramaShotImageReference{ref})
	if err != nil {
		return ref, err
	}
	return resolved[0], nil
}

func (s *Service) applyDramaShotImageReferenceAction(userID, projectID string, shot *model.DramaShot, action DramaShotImageReferenceAction) error {
	resourceID := strings.TrimSpace(action.ResourceID)
	if resourceID == "" {
		return BadAuthRequest("镜头图片资源缺失")
	}
	refs := dramaShotImages(*shot)
	index := -1
	for i := range refs {
		if refs[i].ResourceID == resourceID {
			index = i
			break
		}
	}
	if index < 0 {
		return BadAuthRequest("镜头图片绑定已变化，请刷新后重试")
	}
	switch action.Operation {
	case "remove":
		refs = append(refs[:index], refs[index+1:]...)
	case "refresh_canvas":
		refreshed, err := s.refreshDramaShotCanvasImage(userID, projectID, refs[index])
		if err != nil {
			return err
		}
		for i := range refs {
			if i != index && refs[i].ResourceID == refreshed.ResourceID {
				return BadAuthRequest("画布当前图片已绑定到这个镜头")
			}
		}
		refs[index] = refreshed
	default:
		return BadAuthRequest("镜头图片操作无效")
	}
	data, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	shot.ImageReferencesJSON = string(data)
	return nil
}

func (s *Service) applyDramaShotImages(userID, projectID string, shot *model.DramaShot, edit DramaShotEdit) error {
	if edit.ImageReferences == nil {
		return nil
	}
	refs, err := s.resolveDramaShotImages(userID, projectID, *edit.ImageReferences)
	if err != nil {
		return err
	}
	data, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	shot.ImageReferencesJSON = string(data)
	return nil
}

func dramaShotImages(shot model.DramaShot) []model.DramaShotImageReference {
	refs := []model.DramaShotImageReference{}
	_ = json.Unmarshal([]byte(shot.ImageReferencesJSON), &refs)
	return refs
}

func dramaExplicitFirstFrame(shots []model.DramaShot, index int) bool {
	for _, shot := range shots {
		if shot.SegmentIndex == index {
			for _, ref := range dramaShotImages(shot) {
				if ref.Role == "first_frame" {
					return true
				}
			}
		}
	}
	return false
}

// DryRun computes the exact provider reference plan from cloned shots; it never writes receipts or formal rows.
func (s *Service) BindDramaShotImages(userID string, req DramaShotImagesRequest) (map[string]any, error) {
	if !req.DryRun {
		refs := req.References
		return s.EditDramaShots(userID, req.DramaID, req.EpisodeID, EditDramaShotsRequest{ExpectedEpisodeRevision: req.ExpectedEpisodeRevision, IdempotencyKey: req.IdempotencyKey, Edits: []DramaShotEdit{{Operation: "update", ShotID: req.ShotID, ImageReferences: &refs}}})
	}
	_, p, err := s.dramaForUser(userID, req.DramaID)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.DramaEpisode(req.DramaID, req.EpisodeID); err != nil {
		return nil, err
	}
	shots, err := s.repo.DramaShots(req.EpisodeID)
	if err != nil {
		return nil, err
	}
	if req.ExpectedEpisodeRevision == "" || dramaShotRevision(shots, -1) != req.ExpectedEpisodeRevision {
		return nil, dramaMCPConflict(map[string]any{"episodeRevision": dramaShotRevision(shots, -1)})
	}
	index := -1
	for i := range shots {
		if shots[i].ID == req.ShotID {
			index = i
		}
	}
	if index < 0 {
		return nil, NotFound("镜头不存在")
	}
	refs := req.References
	beforeShots := append([]model.DramaShot(nil), shots...)
	if err := s.applyDramaShotImages(userID, req.DramaID, &shots[index], DramaShotEdit{ImageReferences: &refs}); err != nil {
		return nil, err
	}
	rows, err := s.repo.DramaSegments(req.EpisodeID)
	if err != nil {
		return nil, err
	}
	casts, err := s.repo.DramaCasts(req.DramaID)
	if err != nil {
		return nil, err
	}
	byID := map[string]model.DramaCast{}
	for _, cast := range casts {
		byID[cast.ID] = cast
	}
	segmentIndex := shots[index].SegmentIndex
	indices := map[int]bool{segmentIndex: true}
	beforePlans := s.dramaShotImagePlans(p, beforeShots, byID, rows, indices)
	afterPlans := s.dramaShotImagePlans(p, shots, byID, rows, indices)
	plan, ok := afterPlans[segmentIndex]
	if !ok {
		return nil, NotFound("剧情段不存在")
	}
	for _, row := range rows {
		if row.SegmentIndex == segmentIndex {
			report := dramaReferenceNumberingChanges(segmentIndex, beforePlans[segmentIndex], plan, firstNonEmpty(row.VideoPromptOverride, row.VideoPrompt))
			return map[string]any{"dryRun": true, "shotId": req.ShotID, "segmentIndex": segmentIndex, "episodeRevision": req.ExpectedEpisodeRevision, "proposedShotRevision": dramaShotRevision(shots, segmentIndex), "request": plan.Request, "preparationError": report.AfterPreparationError, "referencePreparationOK": plan.Error == nil, "referenceChanges": []dramaReferenceNumberingReport{report}, "wouldRequirePromptReview": true, "canSubmit": false, "note": "只读模拟；图片送模不代表文字、读数或动作验收。未保存绑定、未生成或采用。"}, nil
		}
	}
	return nil, NotFound("剧情段不存在")
}

func (s *Service) DramaCanvasImageCandidates(userID string, req DramaCanvasImageCandidatesRequest) (map[string]any, error) {
	if req.Offset < 0 || req.Limit < 0 || req.Limit > 100 || len(req.ResourceID) > 80 {
		return nil, BadAuthRequest("offset须非负，limit须为1–100（省略为100），resourceId最多80字节")
	}
	if req.Limit == 0 {
		req.Limit = 100
	}
	projectID, episodeID, canvasID := req.DramaID, req.EpisodeID, req.CanvasID
	if _, _, err := s.dramaForUser(userID, projectID); err != nil {
		return nil, err
	}
	if _, err := s.repo.DramaEpisode(projectID, episodeID); err != nil {
		return nil, err
	}
	canvas, err := s.repo.CanvasProjectForUser(userID, canvasID)
	if err != nil {
		return nil, dramaLookupError(err)
	}
	if canvas.ProjectID != projectID {
		return nil, Forbidden("画布不属于当前作品")
	}
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		return nil, err
	}
	snapshot := cloudAgentCanvasHash(doc)
	if req.SnapshotHash != "" && req.SnapshotHash != snapshot {
		err := dramaMCPConflict(map[string]any{"canvasId": canvasID, "snapshotHash": snapshot, "offset": 0})
		err.Message = "画布快照已变化，请从 offset=0 重新读取候选图片"
		return nil, err
	}
	nodes, err := creationObjects(doc["nodes"])
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := []map[string]any{}
	for _, id := range ids {
		node := nodes[id]
		if node["type"] != "image" {
			continue
		}
		meta, _ := node["metadata"].(map[string]any)
		if req.ResourceID != "" && stringValue(meta["storageKey"]) != "resource:"+req.ResourceID {
			continue
		}
		media, field, err := cloudAgentMediaReference(s.repo, userID, projectID, node)
		if err != nil || field != "referenceImages" {
			continue
		}
		items = append(items, map[string]any{"canvasId": canvasID, "nodeId": id, "nodeHash": cloudAgentNodeHash(doc, id), "resourceId": strings.TrimPrefix(stringValue(media["storageKey"]), "resource:"), "title": truncateRunes(stringValue(node["title"]), 120)})
	}
	start := min(req.Offset, len(items))
	end := start + min(req.Limit, len(items)-start)
	return map[string]any{"images": items[start:end], "offset": req.Offset, "limit": req.Limit, "nextOffset": end, "hasMore": end < len(items), "total": len(items), "snapshotHash": snapshot, "resourceId": req.ResourceID, "note": fmt.Sprintf("按nodeId排序，offset按筛选后已就绪图片计数；仅有画布节点不会自动送入镜头请求。画布节点数 %d", len(nodes))}, nil
}
