package model

import "time"

// Checks are append-only evidence for one immutable audio version and one exact media resource.
type DramaAudioCheck struct {
	ID             string    `json:"id" gorm:"primaryKey;size:36"`
	ProjectID      string    `json:"projectId" gorm:"index;size:36"`
	EpisodeID      string    `json:"episodeId" gorm:"index;size:36"`
	SegmentID      string    `json:"segmentId" gorm:"index;size:36"`
	VersionID      string    `json:"versionId" gorm:"index;size:36;uniqueIndex:idx_drama_audio_check_sequence,priority:1"`
	Sequence       int64     `json:"sequence" gorm:"uniqueIndex:idx_drama_audio_check_sequence,priority:2"`
	CueID          string    `json:"cueId" gorm:"size:80"`
	Layer          string    `json:"layer" gorm:"size:24"`
	Status         string    `json:"status" gorm:"size:24"`
	Method         string    `json:"method" gorm:"size:24"`
	ResourceID     string    `json:"resourceId" gorm:"size:36"`
	ResourceSHA256 string    `json:"resourceSha256,omitempty" gorm:"size:64"`
	ResultJSON     string    `json:"-" gorm:"type:text"`
	ReviewerID     string    `json:"reviewerId" gorm:"size:36"`
	CreatedAt      time.Time `json:"createdAt"`
}
