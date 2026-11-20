package repository

import (
	"database/sql"

	"gorm.io/gorm"
	"yingce/backend/internal/model"
)

type DramaTimingSnapshot struct {
	Production  model.DramaProduction
	Episode     model.DramaEpisode
	Shots       []model.DramaShot
	Segments    []model.DramaSegment
	Casts       []model.DramaCast
	Audio       []model.DramaAudioVersion
	AudioChecks []model.DramaAudioCheck
}

// One read-only snapshot keeps script, segment and audio revisions consistent
// without allocating a mutation receipt or acquiring the episode's write lock.
func (r *Repository) DramaTimingSnapshot(projectID, episodeID string) (DramaTimingSnapshot, error) {
	var result DramaTimingSnapshot
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project_id = ?", projectID).First(&result.Production).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id = ? AND id = ?", projectID, episodeID).First(&result.Episode).Error; err != nil {
			return err
		}
		for _, query := range []struct {
			table any
			order string
		}{
			{&result.Shots, "position, id"}, {&result.Segments, "segment_index, id"}, {&result.Audio, "revision DESC, id"}, {&result.AudioChecks, "sequence DESC, id DESC"},
		} {
			if err := tx.Where("project_id = ? AND episode_id = ?", projectID, episodeID).Order(query.order).Find(query.table).Error; err != nil {
				return err
			}
		}
		return tx.Where("project_id = ?", projectID).Order("id").Find(&result.Casts).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
