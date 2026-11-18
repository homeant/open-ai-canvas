package app

import "yingce/backend/internal/repository"

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
		known := map[int]int64{}
		for _, row := range rows {
			known[row.SegmentIndex] = row.Revision
		}
		for _, index := range req.Request.Segments {
			expected, ok := req.ExpectedSegmentRevisions[itoa(index)]
			if len(req.Request.Segments) == 1 && req.Request.ExpectedPromptRevision != nil {
				expected, ok = *req.Request.ExpectedPromptRevision, true
			}
			current, found := known[index]
			if !ok || !found || expected != current {
				return nil, repository.ErrCreationConflict
			}
		}
		return local.GenerateDramaSegments(userID, req.DramaID, req.EpisodeID, req.Request)
	})
	if err == nil {
		s.wakeDramaScheduler()
	}
	return result, err
}
