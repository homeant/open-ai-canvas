package app

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

type DramaShotMediaScope struct {
	DramaID   string `json:"dramaId"`
	EpisodeID string `json:"episodeId"`
	ShotID    string `json:"shotId"`
}
type RegisterDramaShotMediaRequest struct {
	DramaShotMediaScope
	ExpectedShotRevision string `json:"expectedShotRevision"`
	ResourceID           string `json:"resourceId"`
	SourceOffsetMs       int64  `json:"sourceOffsetMs"`
	DurationMs           int64  `json:"durationMs"`
	IdempotencyKey       string `json:"idempotencyKey"`
}
type DramaShotMediaReviewRequest struct {
	DramaShotMediaScope
	TakeID               string `json:"takeId"`
	ExpectedShotRevision string `json:"expectedShotRevision"`
	ExpectedReviewID     string `json:"expectedReviewId"`
	Layer                string `json:"layer"`
	Status               string `json:"status"`
	Reason               string `json:"reason"`
	StartMs              int64  `json:"startMs"`
	EndMs                int64  `json:"endMs"`
	IdempotencyKey       string `json:"idempotencyKey"`
}
type dramaShotMediaOrigin struct {
	SourceOffsetMs    int64  `json:"sourceOffsetMs"`
	DurationMs        int64  `json:"durationMs"`
	MediaKind         string `json:"mediaKind"`
	PlannedDurationMs int64  `json:"plannedDurationMs"`
}

func (s *Service) dramaShotMediaTarget(userID string, scope DramaShotMediaScope) (*model.DramaShot, []model.DramaShot, error) {
	if _, _, err := s.dramaForUser(userID, scope.DramaID); err != nil {
		return nil, nil, err
	}
	if _, err := s.repo.DramaEpisode(scope.DramaID, scope.EpisodeID); err != nil {
		return nil, nil, err
	}
	shot, err := s.repo.DramaShot(scope.DramaID, scope.ShotID)
	if err != nil {
		return nil, nil, dramaLookupError(err)
	}
	if shot.EpisodeID != scope.EpisodeID {
		return nil, nil, Forbidden("镜头不属于本集")
	}
	shots, err := s.repo.DramaShots(scope.EpisodeID)
	return shot, shots, err
}

func (s *Service) RegisterDramaShotMedia(userID string, req RegisterDramaShotMediaRequest) (map[string]any, error) {
	return s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "shot.media.register", req.IdempotencyKey, req, func(local *Service) (any, error) {
		shot, _, err := local.dramaShotMediaTarget(userID, req.DramaShotMediaScope)
		if err != nil {
			return nil, err
		}
		revision := dramaShotRevision([]model.DramaShot{*shot}, -1)
		if req.ExpectedShotRevision == "" || revision != req.ExpectedShotRevision {
			return nil, dramaMCPConflict(map[string]any{"shotRevision": revision})
		}
		resource, err := local.repo.LockOwnedDramaMedia(userID, req.ResourceID)
		if err != nil {
			return nil, dramaLookupError(err)
		}
		if (resource.Kind != "image" && resource.Kind != "video") || !strings.HasPrefix(resource.MimeType, resource.Kind+"/") {
			return nil, BadAuthRequest("候选版本须为自有已就绪图片或视频")
		}
		if req.DurationMs <= 0 || req.DurationMs > 60000 || req.SourceOffsetMs < 0 || req.SourceOffsetMs > 3600000 {
			return nil, BadAuthRequest("候选裁切时窗无效")
		}
		if resource.Kind == "image" && (req.SourceOffsetMs != 0 || req.DurationMs != shot.DurationMs) {
			return nil, BadAuthRequest("静图仅按本镜计划时长展示，不能宣称动态片段")
		}
		if resource.Kind == "video" && (resource.DurationMs <= 0 || req.SourceOffsetMs+req.DurationMs > resource.DurationMs) {
			return nil, BadAuthRequest("候选视频时长不足或未知")
		}
		origin := dramaShotMediaOrigin{req.SourceOffsetMs, req.DurationMs, resource.Kind, shot.DurationMs}
		data, _ := json.Marshal(origin)
		take := model.DramaTake{ID: newID(), ProjectID: req.DramaID, EpisodeID: req.EpisodeID, OwnerID: req.ShotID, Kind: "shot_media", ResourceID: req.ResourceID, DurationMs: req.DurationMs, ShotRevision: revision, GenerationJSON: string(data), CreatedAt: time.Now()}
		if err := local.repo.CreateDramaTake(&take); err != nil {
			return nil, err
		}
		return map[string]any{"take": take, "qaStatus": "unreviewed", "current": false, "adopted": false, "requiresTimeMapping": req.DurationMs != shot.DurationMs, "note": "仅登记镜头候选，不裁剪文件、不改正式视频或剧本；配音不能自动沿用整集绝对时间。"}, nil
	})
}

func (s *Service) shotMediaWindow(userID string, scope DramaShotMediaScope, take *model.DramaTake) (*model.DramaShot, int64, int64, string, string, error) {
	shot, shots, err := s.dramaShotMediaTarget(userID, scope)
	if err != nil {
		return nil, 0, 0, "", "", err
	}
	start, end, match, kind := int64(0), shot.DurationMs, "unknown", "video"
	switch take.Kind {
	case "shot_media":
		if take.OwnerID != shot.ID || take.EpisodeID != scope.EpisodeID {
			return nil, 0, 0, "", "", BadAuthRequest("版本不属于该镜头")
		}
		var origin dramaShotMediaOrigin
		if err := json.Unmarshal([]byte(take.GenerationJSON), &origin); err != nil {
			return nil, 0, 0, "", "", err
		}
		start, end, kind = origin.SourceOffsetMs, origin.SourceOffsetMs+origin.DurationMs, origin.MediaKind
		match = "stale"
		if take.ShotRevision == dramaShotRevision([]model.DramaShot{*shot}, -1) {
			match = "matched"
		}
	case "video":
		segment, err := s.repo.DramaSegment(scope.DramaID, take.OwnerID)
		if err != nil || segment == nil || segment.EpisodeID != scope.EpisodeID || segment.SegmentIndex != shot.SegmentIndex {
			return nil, 0, 0, "", "", BadAuthRequest("段版本不包含该镜头")
		}
		for _, row := range shots {
			if row.SegmentIndex == shot.SegmentIndex {
				if row.ID == shot.ID {
					break
				}
				start += row.DurationMs
			}
		}
		end = start + shot.DurationMs
		if take.ShotRevision != "" {
			match = "stale"
			if take.ShotRevision == dramaShotRevision(shots, shot.SegmentIndex) {
				match = "matched"
			}
		}
	default:
		return nil, 0, 0, "", "", BadAuthRequest("版本不是镜头候选或本段视频")
	}
	return shot, start, end, match, kind, nil
}

func (s *Service) dramaVisualQA(projectID, takeID, shotID, currentRevision string, still bool) (map[string]any, error) {
	rows, err := s.repo.DramaShotMediaReviews(projectID, takeID)
	if err != nil {
		return nil, err
	}
	latest := map[string]model.DramaShotMediaReview{}
	history := []model.DramaShotMediaReview{}
	for _, row := range rows {
		if shotID != "" && row.ShotID != shotID {
			continue
		}
		history = append(history, row)
		key := row.ShotID + ":" + row.Layer
		if _, ok := latest[key]; !ok {
			latest[key] = row
		}
	}
	status := "unreviewed"
	complete := shotID != ""
	layers := []string{"picture", "text", "continuity"}
	if still {
		layers = layers[:2]
	}
	for _, row := range latest {
		if row.Status == "rejected" {
			status = "rejected"
		}
		if currentRevision != "" && row.ShotRevision != currentRevision {
			complete = false
		}
	}
	for _, layer := range layers {
		if latest[shotID+":"+layer].Status != "approved" {
			complete = false
		}
	}
	if status != "rejected" && complete {
		status = "approved"
		if still {
			status = "still_approved"
		}
	}
	return map[string]any{"status": status, "acceptedForVideo": status == "approved", "reviews": history, "method": "manual", "automaticVisualCheckAvailable": false}, nil
}

func (s *Service) DramaShotMediaVersions(userID string, scope DramaShotMediaScope) (map[string]any, error) {
	shot, shots, err := s.dramaShotMediaTarget(userID, scope)
	if err != nil {
		return nil, err
	}
	list, err := s.repo.DramaTakes(scope.DramaID, scope.ShotID, "shot_media", 50)
	if err != nil {
		return nil, err
	}
	segments, err := s.repo.DramaSegments(scope.EpisodeID)
	if err != nil {
		return nil, err
	}
	for _, segment := range segments {
		if segment.SegmentIndex == shot.SegmentIndex {
			takes, err := s.repo.DramaTakes(scope.DramaID, segment.ID, "video", 50)
			if err != nil {
				return nil, err
			}
			list = append(list, takes...)
		}
	}
	versions := []map[string]any{}
	for _, take := range list {
		_, start, end, match, kind, err := s.shotMediaWindow(userID, scope, &take)
		if err != nil {
			return nil, err
		}
		qa, err := s.dramaVisualQA(scope.DramaID, take.ID, shot.ID, dramaShotRevision([]model.DramaShot{*shot}, -1), kind == "image")
		if err != nil {
			return nil, err
		}
		if match != "matched" && qa["status"] != "rejected" {
			qa["status"], qa["acceptedForVideo"] = match, false
		}
		versions = append(versions, map[string]any{"take": take, "sourceStartMs": start, "sourceEndMs": end, "mediaKind": kind, "scriptMatch": match, "qa": qa, "current": false, "adopted": false, "requiresTimeMapping": take.DurationMs != shot.DurationMs})
	}
	return map[string]any{"shotId": shot.ID, "shotRevision": dramaShotRevision([]model.DramaShot{*shot}, -1), "episodeRevision": dramaShotRevision(shots, -1), "imageReferences": dramaShotImages(*shot), "versions": versions}, nil
}

func (s *Service) ReviewDramaShotMedia(userID string, req DramaShotMediaReviewRequest) (map[string]any, error) {
	return s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "shot.media.review", req.IdempotencyKey, req, func(local *Service) (any, error) {
		take, err := local.repo.DramaTake(req.DramaID, req.TakeID)
		if err != nil {
			return nil, dramaLookupError(err)
		}
		shot, start, end, match, kind, err := local.shotMediaWindow(userID, req.DramaShotMediaScope, take)
		if err != nil {
			return nil, err
		}
		revision := dramaShotRevision([]model.DramaShot{*shot}, -1)
		if req.ExpectedShotRevision == "" || revision != req.ExpectedShotRevision {
			return nil, dramaMCPConflict(map[string]any{"shotRevision": revision})
		}
		if req.Layer != "picture" && req.Layer != "text" && req.Layer != "continuity" {
			return nil, BadAuthRequest("视觉层须为 picture/text/continuity")
		}
		if kind == "image" && req.Layer == "continuity" {
			return nil, BadAuthRequest("静图不能验证动态物理连续性")
		}
		if req.Status != "approved" && req.Status != "rejected" && req.Status != "unknown" {
			return nil, BadAuthRequest("验收状态无效")
		}
		if strings.TrimSpace(req.Reason) == "" || utf8.RuneCountInString(req.Reason) > 2000 {
			return nil, BadAuthRequest("须填写可核对的观察或退回原因")
		}
		if req.StartMs != start || req.EndMs != end {
			return nil, BadAuthRequest("验收须覆盖明确版本的完整镜头裁切窗")
		}
		resource, err := local.repo.LockOwnedDramaMedia(userID, take.ResourceID)
		if err != nil {
			return nil, err
		}
		if req.Status == "approved" && (match != "matched" || (kind == "video" && (resource.DurationMs <= 0 || end > resource.DurationMs))) {
			return nil, BadAuthRequest("版本与现行镜头或实际时长未匹配，不能验收通过；可保留拒绝证据")
		}
		rows, err := local.repo.DramaShotMediaReviews(req.DramaID, take.ID)
		if err != nil {
			return nil, err
		}
		latest := ""
		for _, row := range rows {
			if row.ShotID == req.ShotID && row.Layer == req.Layer {
				latest = row.ID
				break
			}
		}
		if latest != req.ExpectedReviewID {
			return nil, dramaMCPConflict(map[string]any{"reviewId": latest})
		}
		if len(rows) >= 10000 {
			return nil, BadAuthRequest("该版本检查次数已达上限")
		}
		row := model.DramaShotMediaReview{ID: newID(), ProjectID: req.DramaID, EpisodeID: req.EpisodeID, ShotID: req.ShotID, TakeID: take.ID, ResourceID: take.ResourceID, Sequence: int64(len(rows) + 1), Layer: req.Layer, Status: req.Status, Reason: strings.TrimSpace(req.Reason), StartMs: start, EndMs: end, ShotRevision: revision, ReviewerID: userID, CreatedAt: time.Now()}
		if err := local.repo.CreateDramaShotMediaReview(&row); err != nil {
			return nil, err
		}
		return row, nil
	})
}
