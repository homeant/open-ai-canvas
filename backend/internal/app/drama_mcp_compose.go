package app

import (
	"time"

	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

type ComposeDramaMCPRequest struct {
	DramaID          string `json:"dramaId"`
	EpisodeID        string `json:"episodeId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

// ComposeDramaEpisodeMCP returns a bounded receipt; episode details remain a separate read.
func (s *Service) ComposeDramaEpisodeMCP(userID string, req ComposeDramaMCPRequest) (map[string]any, error) {
	result, err := s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "compose", req.IdempotencyKey, req, func(local *Service) (any, error) {
		episode, err := local.repo.DramaEpisode(req.DramaID, req.EpisodeID)
		if err != nil {
			return nil, err
		}
		if episode.Revision != req.ExpectedRevision {
			return nil, repository.ErrCreationConflict
		}
		segments, err := local.repo.DramaSegments(req.EpisodeID)
		if err != nil {
			return nil, err
		}
		shots, err := local.repo.DramaShots(req.EpisodeID)
		if err != nil {
			return nil, err
		}
		for _, segment := range segments {
			takes, err := local.repo.DramaTakes(req.DramaID, segment.ID, "video", dramaTakeListLimit)
			if err != nil {
				return nil, err
			}
			for _, take := range takes {
				if take.ResourceID == segment.VideoResourceID && take.ShotRevision != "" && take.ShotRevision != dramaShotRevision(shots, segment.SegmentIndex) {
					return nil, BadAuthRequest("当前视频与现行分镜不一致，不能合成")
				}
			}
		}
		detail, err := local.ComposeDramaEpisode(userID, req.DramaID, req.EpisodeID)
		if err != nil {
			return nil, err
		}
		jobs, err := local.repo.DramaJobsByStatus(req.DramaID, model.DramaJobQueued, model.DramaJobRunning)
		if err != nil {
			return nil, err
		}
		var job *model.DramaJob
		for i := range jobs {
			if jobs[i].EpisodeID == req.EpisodeID && jobs[i].Kind == model.DramaJobRender {
				job = &jobs[i]
				break
			}
		}
		if job == nil {
			job = &model.DramaJob{ID: newID(), ProjectID: req.DramaID, EpisodeID: req.EpisodeID, Kind: model.DramaJobRender, Status: model.DramaJobQueued, Label: "MCP 合成剧集", CreatedAt: time.Now(), UpdatedAt: time.Now()}
			if err := local.repo.CreateDramaJobs([]model.DramaJob{*job}); err != nil {
				return nil, err
			}
		}
		return map[string]any{"episodeId": req.EpisodeID, "jobId": job.ID, "taskId": job.TaskID, "revision": detail.Episode.Revision, "finalResourceId": detail.Episode.FinalResourceID, "finalStale": detail.Episode.FinalStale, "status": job.Status}, nil
	})
	if err == nil {
		s.wakeDramaScheduler()
	}
	return result, err
}
