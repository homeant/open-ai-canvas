package repository

import (
	"sort"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"yingce/backend/internal/model"
)

func (r *Repository) LockDramaAudioBudget(projectID string) error {
	return r.db.Model(&model.DramaProduction{}).Where("project_id = ?", projectID).UpdateColumn("spent_microcredits", gorm.Expr("spent_microcredits")).Error
}

func (r *Repository) DramaAudioVersions(projectID, segmentID string, revision int64) ([]model.DramaAudioVersion, error) {
	rows := []model.DramaAudioVersion{}
	q := r.db.Where("project_id = ? AND segment_id = ?", projectID, segmentID)
	if revision > 0 {
		q = q.Where("revision = ?", revision)
	}
	err := q.Order("revision DESC").Limit(50).Find(&rows).Error
	return rows, err
}

// The caller holds DramaMutation's episode lock; the unique index also guards revision allocation.
func (r *Repository) CreateDramaAudioVersion(row *model.DramaAudioVersion, resourceIDs []string) error {
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range resourceIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var resource model.Resource
		if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND status = ?", id, row.CreatedBy, model.ResourceStatusReady).First(&resource).Error; err != nil {
			return err
		}
	}
	if err := r.db.Create(row).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := r.db.Create(&model.DramaAudioResourceLink{VersionID: row.ID, ResourceID: id, ProjectID: row.ProjectID, EpisodeID: row.EpisodeID}).Error; err != nil {
			return err
		}
	}
	return nil
}
