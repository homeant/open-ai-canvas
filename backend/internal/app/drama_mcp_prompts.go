package app

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"yingce/backend/internal/model"
	"yingce/backend/internal/production"
	"yingce/backend/internal/repository"
)

type DramaMCPPromptRequest struct {
	DramaID                 string            `json:"dramaId"`
	EpisodeID               string            `json:"episodeId"`
	SegmentIndex            int               `json:"segmentIndex"`
	ExpectedSegmentRevision int64             `json:"expectedSegmentRevision"`
	ExpectedShotRevision    string            `json:"expectedShotRevision"`
	IdempotencyKey          string            `json:"idempotencyKey"`
	Model                   *DramaModelChoice `json:"model,omitempty"`
	Prompt                  string            `json:"prompt,omitempty"`
	CandidateID             string            `json:"candidateId,omitempty"`
	ExpectedSeconds         int               `json:"expectedSeconds,omitempty"`
}

func (s *Service) dramaMCPPromptTarget(userID string, req DramaMCPPromptRequest) (DramaSegmentDetailResult, *model.DramaSegment, error) {
	detail, err := s.DramaSegmentDetail(userID, req.DramaID, req.EpisodeID, req.SegmentIndex)
	if err != nil {
		return detail, nil, err
	}
	if detail.Segment.Revision != req.ExpectedSegmentRevision || req.ExpectedShotRevision == "" || dramaShotRevision(detail.Shots, req.SegmentIndex) != req.ExpectedShotRevision {
		return detail, nil, dramaMCPConflict(map[string]any{"segmentRevision": detail.Segment.Revision, "shotRevision": detail.Segment.ShotRevision})
	}
	row, err := s.repo.DramaSegment(req.DramaID, detail.Segment.ID)
	return detail, row, err
}

// MutateDramaSegmentPrompt shares the transaction and replay semantics of shot editing.
func (s *Service) MutateDramaSegmentPrompt(userID, action string, req DramaMCPPromptRequest) (map[string]any, error) {
	result, err := s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "prompt_"+action, req.IdempotencyKey, req, func(local *Service) (any, error) {
		detail, row, err := local.dramaMCPPromptTarget(userID, req)
		if err != nil {
			return nil, err
		}
		switch action {
		case "rewrite":
			_, p, err := local.dramaForUser(userID, req.DramaID)
			if err != nil {
				return nil, err
			}
			choice := parseDramaSettings(p.SettingsJSON).Models.textFor(DramaStepPrompt)
			if req.Model != nil {
				choice = *req.Model
				if choice.Empty() {
					return nil, BadAuthRequest("需要有效的文本模型")
				}
				if err := local.validateDramaModelOverrides(userID, DramaModels{PromptText: choice}); err != nil {
					return nil, err
				}
			}
			encoded, _ := json.Marshal(choice)
			fields := map[string]any{"prompt_status": "", "prompt_error": "", "candidate_id": "", "candidate_shot_revision": "", "require_prompt_adoption": true, "prompt_model_json": string(encoded)}
			if row.VideoPromptOverride == "" && row.VideoPrompt != "" {
				fields["video_prompt_override"] = row.VideoPrompt
			}
			if err := local.repo.CommitDramaPrompt(*row, fields, ""); err != nil {
				return nil, err
			}
			job := model.DramaJob{ID: newID(), ProjectID: req.DramaID, EpisodeID: req.EpisodeID, ShotID: row.ID, Kind: model.DramaJobSegmentPrompt, Status: model.DramaJobQueued, Label: "MCP 重写段提示词", CreatedAt: time.Now(), UpdatedAt: time.Now()}
			if err := local.repo.CreateDramaJobs([]model.DramaJob{job}); err != nil {
				return nil, err
			}
			if err := local.requestDramaWork(p); err != nil {
				return nil, err
			}
			return map[string]any{"jobId": job.ID, "taskId": nil, "taskStatus": "pending_dispatch", "segmentRevision": row.Revision + 1, "shotRevision": req.ExpectedShotRevision, "model": choice, "requiresAdoption": true}, nil
		case "edit", "adopt", "confirm":
			if req.ExpectedSeconds != detail.Segment.Request.Seconds {
				return nil, NewAppError(409, "时长档位已改变，需使用当前 request.seconds")
			}
			prompt := firstNonEmpty(row.VideoPromptOverride, row.VideoPrompt)
			if action == "edit" {
				prompt = production.NormalizeReferenceTokens(strings.TrimSpace(req.Prompt))
				if prompt == "" || utf8.RuneCountInString(prompt) > 12000 {
					return nil, BadAuthRequest("段提示词须为 1–12000 字")
				}
			}
			if action == "adopt" {
				if row.PromptSeconds != detail.Segment.Request.Seconds {
					return nil, NewAppError(409, "候选提示词时长档位与当前视频模型不一致，请重新撰写")
				}
				if row.CandidateID == "" || req.CandidateID != row.CandidateID || row.CandidateShotRevision != req.ExpectedShotRevision || row.PromptStatus != model.DramaPromptReview {
					return nil, repository.ErrCreationConflict
				}
				prompt = row.VideoPrompt
			}
			if prompt == "" {
				return nil, BadAuthRequest("没有可确认的提示词")
			}
			if req.Model == nil || dramaChoiceCacheKey(*req.Model) != dramaChoiceCacheKey(detail.Segment.VideoModel) {
				return nil, BadAuthRequest("需明确使用当前 videoModel 核对生成模型")
			}
			conflicts := dramaPromptConflicts(detail.Segment, prompt)
			if action == "edit" && row.PromptSeconds > 0 && row.PromptSeconds != detail.Segment.Request.Seconds {
				conflicts = append(conflicts, "旧提示词时长档位已失效，请按当前 seconds 核对正文后显式确认")
			}
			if action != "edit" && len(conflicts) > 0 {
				return nil, BadAuthRequest(strings.Join(conflicts, "；"))
			}
			status := model.DramaPromptConflict
			fields := map[string]any{"video_prompt_override": prompt, "prompt_status": status, "prompt_error": "人工改稿待显式确认", "candidate_id": "", "candidate_shot_revision": "", "prompt_revision": row.PromptRevision + 1}
			if action != "edit" {
				status = model.DramaMediaReady
				fields["prompt_status"] = status
				fields["prompt_error"] = ""
				fields["prompt_seconds"] = detail.Segment.Request.Seconds
				fields["prompt_shot_revision"] = req.ExpectedShotRevision
				fields["prompt_revision"] = row.PromptRevision + 1
				fields["require_prompt_adoption"] = false
			}
			if err := local.repo.CommitDramaPrompt(*row, fields, prompt); err != nil {
				return nil, err
			}
			return map[string]any{"segmentRevision": row.Revision + 1, "promptRevision": row.PromptRevision + 1, "shotRevision": req.ExpectedShotRevision, "promptStatus": status, "fullPrompt": strings.TrimSpace(detail.Segment.Request.Header + "\n\n" + prompt), "seconds": detail.Segment.Request.Seconds, "model": detail.Segment.VideoModel, "conflicts": conflicts}, nil
		default:
			return nil, BadAuthRequest("提示词操作无效")
		}
	})
	if err == nil && action == "rewrite" {
		s.wakeDramaScheduler()
	}
	return result, err
}

func dramaPromptConflicts(segment DramaSegmentView, prompt string) []string {
	conflicts := []string{}
	request := segment.Request
	if request.Seconds <= 0 || segment.DurationMs > int64(request.MaxVideoSeconds)*1000 {
		conflicts = append(conflicts, "分镜时长超过当前模型上限")
	}
	if request.PromptMaxChars > 0 && utf8.RuneCountInString(strings.TrimSpace(request.Header+"\n\n"+prompt)) > request.PromptMaxChars {
		conflicts = append(conflicts, "完整送模提示词超出模型字符上限")
	}
	for _, token := range production.UnknownReferenceTokens(prompt, len(request.References)) {
		conflicts = append(conflicts, "未知参考图: "+token)
	}
	return conflicts
}
