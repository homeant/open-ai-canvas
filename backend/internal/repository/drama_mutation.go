package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"yingce/backend/internal/kernel"
	"yingce/backend/internal/model"
)

var ErrDramaIdempotencyConflict = errors.New("idempotency key reused with different input")

func (r *Repository) DramaMutationReplay(projectID, episodeID, receiptID, hash string) ([]byte, bool, error) {
	var receipt model.DramaMutationReceipt
	err := r.db.Where("id = ? AND project_id = ? AND episode_id = ?", receiptID, projectID, episodeID).First(&receipt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if receipt.RequestHash != hash {
		return nil, false, ErrDramaIdempotencyConflict
	}
	return []byte(receipt.ResultJSON), true, nil
}

// DramaMutation serializes edits to one episode and persists the replay result atomically.
func (r *Repository) DramaMutation(projectID, episodeID, receiptID, hash string, fn func(*Repository) ([]byte, error)) ([]byte, error) {
	var result []byte
	err := r.db.Transaction(func(tx *gorm.DB) error {
		// An UPDATE also acquires SQLite's write lock before reading the snapshot.
		lock := tx.Model(&model.DramaEpisode{}).Where("id = ? AND project_id = ?", episodeID, projectID).UpdateColumn("revision", gorm.Expr("revision"))
		if lock.Error != nil {
			return lock.Error
		}
		if lock.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		var receipt model.DramaMutationReceipt
		err := tx.First(&receipt, "id = ?", receiptID).Error
		if err == nil {
			if receipt.RequestHash != hash {
				return ErrDramaIdempotencyConflict
			}
			result = []byte(receipt.ResultJSON)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// Lock existing input rows as well as the episode for concurrent browser saves.
		var shots []model.DramaShot
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("episode_id = ?", episodeID).Find(&shots).Error; err != nil {
			return err
		}
		var units []model.ProjectUnit
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ?", projectID).Find(&units).Error; err != nil {
			return err
		}
		var segments []model.DramaSegment
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("episode_id = ?", episodeID).Find(&segments).Error; err != nil {
			return err
		}
		result, err = fn(&Repository{db: tx})
		if err != nil {
			return err
		}
		return tx.Create(&model.DramaMutationReceipt{ID: receiptID, ProjectID: projectID, EpisodeID: episodeID, RequestHash: hash, ResultJSON: string(result), CreatedAt: time.Now()}).Error
	})
	return result, err
}

func (r *Repository) ReplaceDramaEditedShots(episode model.DramaEpisode, shots []model.DramaShot, affected map[int]bool) error {
	if err := r.lockDramaShotImageResources(episode.ProjectID, shots); err != nil {
		return err
	}
	before, err := r.DramaShots(episode.ID)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, shot := range before {
		existing[shot.ID] = true
	}
	ids := make([]string, 0, len(shots))
	for _, shot := range shots {
		ids = append(ids, shot.ID)
		if existing[shot.ID] {
			if err := r.db.Model(&model.DramaShot{}).Where("id = ? AND episode_id = ?", shot.ID, episode.ID).Select("*").Updates(&shot).Error; err != nil {
				return err
			}
		} else {
			if err := r.db.Create(&shot).Error; err != nil {
				return err
			}
		}
	}
	if err := r.db.Where("episode_id = ? AND id NOT IN ?", episode.ID, ids).Delete(&model.DramaShot{}).Error; err != nil {
		return err
	}
	segments, err := r.DramaSegments(episode.ID)
	if err != nil {
		return err
	}
	for _, segment := range segments {
		if affected[segment.SegmentIndex] {
			// An earlier edited segment may have cascaded a revision change to this one.
			current, err := r.DramaSegment(episode.ProjectID, segment.ID)
			if err != nil {
				return err
			}
			if err := invalidateDramaSegment(r.db, *current, map[string]any{"prompt_status": model.DramaPromptConflict, "candidate_id": "", "candidate_shot_revision": ""}, true); err != nil {
				return err
			}
		}
	}
	var total int64
	for _, shot := range shots {
		total += shot.DurationMs
	}
	return r.db.Model(&model.DramaEpisode{}).Where("id = ?", episode.ID).Updates(map[string]any{"shot_count": len(shots), "duration_ms": total}).Error
}

func (r *Repository) CommitDramaPrompt(segment model.DramaSegment, fields map[string]any, history string) error {
	if err := invalidateDramaSegment(r.db, segment, fields, true); err != nil {
		return err
	}
	if history != "" {
		return r.db.Create(&model.DramaSegmentPromptRevision{ID: kernel.NewID(), ProjectID: segment.ProjectID, SegmentID: segment.ID, Revision: segment.Revision + 1, Prompt: history, CreatedAt: time.Now()}).Error
	}
	return nil
}
