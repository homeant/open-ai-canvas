package repository

// CurrentDramaMediaAdoptions reports current formal assignments, not historical
// takes, task outputs or a review node merely referring to the same resource.
func (r *Repository) CurrentDramaMediaAdoptions(userID, resourceID string) ([]map[string]any, error) {
	result := []map[string]any{}
	for _, entry := range []struct{ table, column, kind string }{
		{"drama_casts", "image_resource_id", "cast"},
		{"drama_segments", "video_resource_id", "segment"},
		{"drama_episodes", "final_resource_id", "episode"},
	} {
		var rows []struct{ ID, ProjectID string }
		err := r.db.Table(entry.table+" AS media").Select("media.id,media.project_id").Joins("JOIN projects ON projects.id = media.project_id").Where("projects.user_id = ? AND media."+entry.column+" = ?", userID, resourceID).Limit(100).Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			result = append(result, map[string]any{"kind": entry.kind, "id": row.ID, "projectId": row.ProjectID})
		}
	}
	return result, nil
}
