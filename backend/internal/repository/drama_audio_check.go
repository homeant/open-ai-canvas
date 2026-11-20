package repository

import "yingce/backend/internal/model"

func (r *Repository) DramaAudioChecks(projectID, versionID string) ([]model.DramaAudioCheck, error) {
	rows := []model.DramaAudioCheck{}
	err := r.db.Where("project_id = ? AND version_id = ?", projectID, versionID).Order("sequence DESC").Find(&rows).Error
	return rows, err
}

func (r *Repository) CreateDramaAudioCheck(row *model.DramaAudioCheck) error {
	return r.db.Create(row).Error
}
