package app

import "yingce/backend/internal/model"

type GenerateDramaMCPRequest struct {
	DramaID                  string                       `json:"dramaId"`
	EpisodeID                string                       `json:"episodeId"`
	Request                  GenerateDramaSegmentsRequest `json:"request"`
	ExpectedSegmentRevisions map[string]int64             `json:"expectedSegmentRevisions,omitempty"`
	IdempotencyKey           string                       `json:"idempotencyKey"`
}

func (s *Service) GenerateDramaSegmentsMCP(userID string, req GenerateDramaMCPRequest) (map[string]any, error) {
	result, err := s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "generate_segments", req.IdempotencyKey, req, func(local *Service) (any, error) {
		rows, err := local.repo.DramaSegments(req.EpisodeID)
		if err != nil {
			return nil, err
		}
		known := map[int]model.DramaSegment{}
		for _, row := range rows {
			known[row.SegmentIndex] = row
		}
		for _, index := range req.Request.Segments {
			expected, ok := req.ExpectedSegmentRevisions[itoa(index)]
			current, found := known[index]
			promptGuard := len(req.Request.Segments) == 1 && req.Request.ExpectedPromptRevision != nil
			if !found {
				return nil, BadAuthRequest("剧情段不存在")
			}
			if (!ok && !promptGuard) || (ok && expected != current.Revision) || (promptGuard && *req.Request.ExpectedPromptRevision != current.PromptRevision) {
				return nil, local.dramaGenerationConflict(req.EpisodeID, req.Request.Segments)
			}
		}
		return local.GenerateDramaSegments(userID, req.DramaID, req.EpisodeID, req.Request)
	})
	if err == nil {
		s.wakeDramaScheduler()
	}
	return result, err
}

func (s *Service) dramaGenerationConflict(episodeID string, indexes []int) error {
	rows, err := s.repo.DramaSegments(episodeID)
	if err != nil {
		return err
	}
	shots, err := s.repo.DramaShots(episodeID)
	if err != nil {
		return err
	}
	states := []map[string]any{}
	for _, row := range rows {
		for _, index := range indexes {
			if row.SegmentIndex == index {
				states = append(states, map[string]any{"segmentIndex": index, "segmentRevision": row.Revision, "promptRevision": row.PromptRevision, "shotRevision": dramaShotRevision(shots, index), "promptShotRevision": row.PromptShotRevision, "promptStatus": row.PromptStatus})
				break
			}
		}
	}
	details := map[string]any{"segments": states}
	if len(states) == 1 {
		for key, value := range states[0] {
			details[key] = value
		}
	}
	conflict := dramaMCPConflict(details)
	conflict.Reason = ReasonDramaSegmentChanged
	return conflict
}
