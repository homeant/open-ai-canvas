package repository

import (
	"encoding/json"
	"sort"

	"gorm.io/gorm/clause"
	"yingce/backend/internal/model"
)

func (r *Repository) lockDramaShotImageResources(projectID string, shots []model.DramaShot) error {
	var project model.Project
	if err := r.db.First(&project, "id = ?", projectID).Error; err != nil {
		return err
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, shot := range shots {
		if shot.ImageReferencesJSON == "" {
			continue
		}
		var refs []model.DramaShotImageReference
		if err := json.Unmarshal([]byte(shot.ImageReferencesJSON), &refs); err != nil {
			return err
		}
		for _, ref := range refs {
			if !seen[ref.ResourceID] {
				seen[ref.ResourceID] = true
				ids = append(ids, ref.ResourceID)
			}
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		var resource model.Resource
		if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND kind = ? AND status = ?", id, project.UserID, "image", model.ResourceStatusReady).First(&resource).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) DramaShotMediaReviews(projectID, takeID string) ([]model.DramaShotMediaReview, error) {
	rows := []model.DramaShotMediaReview{}
	err := r.db.Where("project_id = ? AND take_id = ?", projectID, takeID).Order("sequence DESC").Find(&rows).Error
	return rows, err
}

func (r *Repository) CreateDramaShotMediaReview(row *model.DramaShotMediaReview) error {
	return r.db.Create(row).Error
}

func (r *Repository) LockOwnedDramaMedia(userID, id string) (*model.Resource, error) {
	var resource model.Resource
	err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND status = ?", id, userID, model.ResourceStatusReady).First(&resource).Error
	return &resource, err
}
