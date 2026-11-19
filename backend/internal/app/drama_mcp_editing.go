package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"yingce/backend/internal/model"
	"yingce/backend/internal/production"
	"yingce/backend/internal/repository"
)

func dramaContentRevision(value any) string {
	data, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func dramaMCPConflict(current map[string]any) *AppError {
	err := NewAppError(409, "内容版本已改变，请重新读取后再提交")
	err.Details = current
	return err
}

// Timestamps and display positions outside this segment do not invalidate its script.
func dramaShotRevision(shots []model.DramaShot, index int) string {
	rows := []model.DramaShot{}
	for _, shot := range shots {
		if index < 0 || shot.SegmentIndex == index {
			shot.CreatedAt, shot.UpdatedAt = time.Time{}, time.Time{}
			shot.Position = len(rows)
			rows = append(rows, shot)
		}
	}
	return dramaContentRevision(rows)
}

type DramaEpisodeSource struct {
	SourceText      string              `json:"sourceText"`
	ReadaptPending  bool                `json:"readaptPending"`
	EpisodeID       string              `json:"episodeId"`
	SourceRevision  string              `json:"sourceRevision"`
	EpisodeRevision string              `json:"episodeRevision"`
	SourceFormat    string              `json:"sourceFormat"`
	Chapters        []model.ProjectUnit `json:"chapters"`
	ReadaptOnSave   bool                `json:"readaptOnSave"`
}

func (s *Service) DramaEpisodeSource(userID, projectID, episodeID string) (DramaEpisodeSource, error) {
	if _, _, err := s.dramaForUser(userID, projectID); err != nil {
		return DramaEpisodeSource{}, err
	}
	episode, err := s.repo.DramaEpisode(projectID, episodeID)
	if err != nil {
		return DramaEpisodeSource{}, err
	}
	result := DramaEpisodeSource{EpisodeID: episodeID, SourceFormat: firstNonEmpty(episode.SourceFormat, production.SourceNovel), Chapters: []model.ProjectUnit{}}
	result.ReadaptPending = episode.Stage == model.DramaEpisodeScript
	type source struct{ ID, Text, Title string }
	content := []source{}
	for _, id := range decodeStringList(episode.ChapterIDsJSON) {
		chapter, err := s.repo.ProjectUnit(projectID, id)
		if err != nil {
			return result, err
		}
		result.Chapters = append(result.Chapters, *chapter)
		if result.SourceText != "" {
			result.SourceText += "\n\n"
		}
		result.SourceText += chapter.SourceText
		content = append(content, source{chapter.ID, chapter.SourceText, chapter.Title})
	}
	result.SourceRevision = dramaContentRevision([]any{result.SourceFormat, content})
	shots, err := s.repo.DramaShots(episodeID)
	if err != nil {
		return result, err
	}
	result.EpisodeRevision = dramaShotRevision(shots, -1)
	return result, nil
}

type UpdateDramaEpisodeSourceRequest struct {
	SourceText             string `json:"sourceText"`
	ChapterID              string `json:"chapterId,omitempty"`
	ExpectedSourceRevision string `json:"expectedSourceRevision"`
	Readapt                bool   `json:"readapt"`
	IdempotencyKey         string `json:"idempotencyKey"`
}

// runDramaMutation scopes keys to the authenticated user, episode and command.
func (s *Service) runDramaMutation(userID, projectID, episodeID, command, key string, input any, fn func(*Service) (any, error)) (map[string]any, error) {
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		return nil, BadAuthRequest("需要 1–200 字节的 idempotencyKey")
	}
	if _, _, err := s.dramaForUser(userID, projectID); err != nil {
		return nil, err
	}
	data, err := s.repo.DramaMutation(projectID, episodeID, dramaContentRevision([]string{userID, projectID, episodeID, command, key}), dramaContentRevision(input), func(repo *repository.Repository) ([]byte, error) {
		// Fresh mutexes/caches, transaction-bound repository; never copy the live Service's locks.
		local := &Service{repo: repo, dataDir: s.dataDir, pluginRuntime: s.pluginRuntime, pluginRuntimeErr: s.pluginRuntimeErr, coordinator: s.coordinator, routeCatalogTTL: s.routeCatalogTTL, routeCatalogMaxStale: s.routeCatalogMaxStale}
		result, err := fn(local)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	})
	if errors.Is(err, repository.ErrCreationConflict) {
		return nil, NewAppError(409, "内容版本已改变，请重新读取后再提交")
	}
	if errors.Is(err, repository.ErrDramaIdempotencyConflict) {
		return nil, NewAppError(409, "幂等键已用于不同请求")
	}
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(data, &result)
	return result, err
}

func (s *Service) UpdateDramaEpisodeSource(userID, projectID, episodeID string, req UpdateDramaEpisodeSourceRequest) (map[string]any, error) {
	result, err := s.runDramaMutation(userID, projectID, episodeID, "source", req.IdempotencyKey, req, func(local *Service) (any, error) {
		before, err := local.DramaEpisodeSource(userID, projectID, episodeID)
		if err != nil {
			return nil, err
		}
		if req.ExpectedSourceRevision == "" || before.SourceRevision != req.ExpectedSourceRevision {
			return nil, dramaMCPConflict(map[string]any{"sourceRevision": before.SourceRevision})
		}
		id := req.ChapterID
		if id == "" && len(before.Chapters) == 1 {
			id = before.Chapters[0].ID
		}
		found := false
		for _, chapter := range before.Chapters {
			if chapter.ID == id {
				found = true
			}
		}
		if !found {
			return nil, BadAuthRequest("多章剧集需指定本集 chapterId")
		}
		// Shared source chapters would silently change other episodes' source contracts.
		episodes, err := local.repo.DramaEpisodes(projectID)
		if err != nil {
			return nil, err
		}
		for _, episode := range episodes {
			if episode.ID != episodeID && containsString(decodeStringList(episode.ChapterIDsJSON), id) {
				return nil, BadAuthRequest("原文章节被其他剧集共用，请先拆分章节")
			}
		}
		if _, err := local.UpdateDramaChapter(userID, projectID, id, UpdateDramaChapterRequest{Text: &req.SourceText}); err != nil {
			return nil, err
		}
		action := "saved_source_only"
		if req.Readapt {
			if _, err := local.RescriptDramaEpisode(userID, projectID, episodeID); err != nil {
				return nil, err
			}
			action = "saved_and_readapt_queued"
		}
		after, err := local.DramaEpisodeSource(userID, projectID, episodeID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"source": after, "action": action, "chapterId": id, "sourceChanged": before.SourceRevision != after.SourceRevision, "shotsPreserved": !req.Readapt}, nil
	})
	if err == nil && req.Readapt {
		s.wakeDramaScheduler()
	}
	return result, err
}

type DramaShotEdit struct {
	Operation    string    `json:"operation"`
	ShotID       string    `json:"shotId"`
	NewShotID    string    `json:"newShotId,omitempty"`
	DialogueMode *string   `json:"dialogueMode,omitempty"`
	Scene        *string   `json:"scene,omitempty"`
	SceneCastID  *string   `json:"sceneCastId,omitempty"`
	CastIDs      *[]string `json:"castIds,omitempty"`
	PropIDs      *[]string `json:"propIds,omitempty"`
	LookCastIDs  *[]string `json:"lookCastIds,omitempty"`
	UpdateDramaShotRequest
}
type EditDramaShotsRequest struct {
	ExpectedEpisodeRevision string          `json:"expectedEpisodeRevision"`
	IdempotencyKey          string          `json:"idempotencyKey"`
	Edits                   []DramaShotEdit `json:"edits"`
}

func applyDramaMCPShotFields(shot *model.DramaShot, edit DramaShotEdit) error {
	for _, field := range []struct {
		target *string
		value  *string
		limit  int
	}{
		{&shot.Speaker, edit.Speaker, 60}, {&shot.Dialogue, edit.Dialogue, dramaMaxDialogueRunes}, {&shot.Action, edit.Action, 1000}, {&shot.ShotSize, edit.ShotSize, 30}, {&shot.Camera, edit.Camera, 60}, {&shot.ImagePrompt, edit.ImagePrompt, 2000}, {&shot.VideoPrompt, edit.VideoPrompt, 2000},
	} {
		if field.value != nil {
			value := strings.TrimSpace(*field.value)
			if utf8.RuneCountInString(value) > field.limit {
				return BadAuthRequest("镜头内容过长")
			}
			*field.target = value
		}
	}
	if edit.DialogueMode != nil {
		switch *edit.DialogueMode {
		case "dialogue", "inner_monologue", "voice_over", "silent":
			shot.DialogueMode = *edit.DialogueMode
		default:
			return BadAuthRequest("dialogueMode 无效")
		}
	}
	if edit.Transition != nil {
		switch *edit.Transition {
		case "cut", "fadeblack", "fadewhite":
			shot.Transition = *edit.Transition
		default:
			return BadAuthRequest("transition 无效")
		}
	}
	if edit.DurationSeconds != nil {
		seconds := *edit.DurationSeconds
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 1 || seconds > production.MaxStoryboardShotSeconds {
			return BadAuthRequest("镜头时长超出范围")
		}
		shot.DurationMs = int64(math.Round(seconds * 1000))
	}
	return nil
}

func (s *Service) EditDramaShots(userID, projectID, episodeID string, req EditDramaShotsRequest) (map[string]any, error) {
	return s.runDramaMutation(userID, projectID, episodeID, "shots", req.IdempotencyKey, req, func(local *Service) (any, error) {
		if len(req.Edits) == 0 || len(req.Edits) > 100 {
			return nil, BadAuthRequest("一次需提交 1–100 个镜头操作")
		}
		episode, err := local.repo.DramaEpisode(projectID, episodeID)
		if err != nil {
			return nil, err
		}
		shots, err := local.repo.DramaShots(episodeID)
		if err != nil {
			return nil, err
		}
		if req.ExpectedEpisodeRevision == "" || req.ExpectedEpisodeRevision != dramaShotRevision(shots, -1) {
			return nil, dramaMCPConflict(map[string]any{"episodeRevision": dramaShotRevision(shots, -1)})
		}
		references := map[string]model.DramaCast{}
		for _, edit := range req.Edits {
			if edit.SceneCastID != nil || edit.CastIDs != nil || edit.PropIDs != nil || edit.LookCastIDs != nil {
				casts, err := local.repo.DramaCasts(projectID)
				if err != nil {
					return nil, err
				}
				for _, cast := range casts {
					references[cast.ID] = cast
				}
				break
			}
		}
		affected := map[int]bool{}
		inserted := []string{}
		for _, edit := range req.Edits {
			pos := -1
			for i := range shots {
				if shots[i].ID == edit.ShotID {
					pos = i
					break
				}
			}
			if pos < 0 {
				return nil, BadAuthRequest("shotId 不属于本集")
			}
			affected[shots[pos].SegmentIndex] = true
			switch edit.Operation {
			case "update":
				if err := applyDramaMCPShotFields(&shots[pos], edit); err != nil {
					return nil, err
				}
				if err := applyDramaMCPShotReferences(&shots[pos], edit, references); err != nil {
					return nil, err
				}
			case "insert_after":
				if edit.DurationSeconds == nil {
					return nil, BadAuthRequest("插镜必须明确 durationSeconds，不创建默认空镜")
				}
				id := edit.NewShotID
				if id == "" {
					id = newID()
				}
				if len(id) > 36 {
					return nil, BadAuthRequest("newShotId 最多 36 字节")
				}
				for _, shot := range shots {
					if shot.ID == id {
						return nil, BadAuthRequest("newShotId 重复")
					}
				}
				anchor := shots[pos]
				shot := model.DramaShot{ID: id, ProjectID: projectID, EpisodeID: episodeID, SegmentIndex: anchor.SegmentIndex, SegmentTitle: anchor.SegmentTitle, Scene: anchor.Scene, SceneCastID: anchor.SceneCastID, CastIDsJSON: anchor.CastIDsJSON, PropIDsJSON: "[]", LookCastIDsJSON: anchor.LookCastIDsJSON, Transition: "cut", DialogueMode: "dialogue", CreatedAt: time.Now()}
				if err := applyDramaMCPShotFields(&shot, edit); err != nil {
					return nil, err
				}
				if edit.DialogueMode == nil && strings.TrimSpace(shot.Speaker) == "" && strings.TrimSpace(shot.Dialogue) == "" {
					shot.DialogueMode = "silent"
				}
				if err := applyDramaMCPShotReferences(&shot, edit, references); err != nil {
					return nil, err
				}
				shots = append(shots, model.DramaShot{})
				copy(shots[pos+2:], shots[pos+1:])
				shots[pos+1] = shot
				inserted = append(inserted, id)
			case "delete":
				shots = append(shots[:pos], shots[pos+1:]...)
			default:
				return nil, BadAuthRequest("operation 需为 insert_after / update / delete")
			}
		}
		if len(shots) > production.MaxScriptShots {
			return nil, BadAuthRequest("单集镜头数量超限")
		}
		totals := map[int]int64{}
		for i := range shots {
			shots[i].Position = i
			shots[i].UpdatedAt = time.Now()
			totals[shots[i].SegmentIndex] += shots[i].DurationMs
		}
		_, p, err := local.dramaForUser(userID, projectID)
		if err != nil {
			return nil, err
		}
		settings := parseDramaSettings(p.SettingsJSON)
		segments, err := local.repo.DramaSegments(episodeID)
		if err != nil {
			return nil, err
		}
		for _, segment := range segments {
			if affected[segment.SegmentIndex] {
				total := totals[segment.SegmentIndex]
				if total == 0 {
					return nil, BadAuthRequest("不能删除剧情段的全部镜头")
				}
				profile, err := local.dramaVideoProfile(segmentVideoChoice(segment, settings.Models.Video))
				if err != nil {
					return nil, err
				}
				limit := int64(profile.maxSeconds()) * 1000
				if total > limit {
					return nil, BadAuthRequest(fmt.Sprintf("第 %d 段(index=%d)合计 %.3f 秒，模型上限 %.3f 秒，超出 %.3f 秒", segment.SegmentIndex+1, segment.SegmentIndex, float64(total)/1000, float64(limit)/1000, float64(total-limit)/1000))
				}
			}
		}
		if err := local.repo.ReplaceDramaEditedShots(*episode, shots, affected); err != nil {
			return nil, err
		}
		return map[string]any{"episodeRevision": dramaShotRevision(shots, -1), "shots": shots, "segmentDurationsMs": totals, "insertedShotIds": inserted, "action": "shots_saved_prompts_require_review"}, nil
	})
}
