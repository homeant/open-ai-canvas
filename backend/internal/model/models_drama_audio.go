package model

import "time"

// DramaAudioVersion is an immutable post-production plan, separate from the current video.
type DramaAudioVersion struct {
	ID              string    `json:"id" gorm:"primaryKey;size:36"`
	ProjectID       string    `json:"projectId" gorm:"index;size:36"`
	EpisodeID       string    `json:"episodeId" gorm:"index;size:36"`
	SegmentID       string    `json:"segmentId" gorm:"size:36;uniqueIndex:idx_drama_audio_version,priority:1"`
	Revision        int64     `json:"revision" gorm:"uniqueIndex:idx_drama_audio_version,priority:2"`
	ShotRevision    string    `json:"shotRevision" gorm:"size:64"`
	VideoTakeID     string    `json:"videoTakeId" gorm:"size:36"`
	VideoResourceID string    `json:"videoResourceId" gorm:"size:36"`
	TracksJSON      string    `json:"-" gorm:"type:text"`
	CreatedBy       string    `json:"createdBy" gorm:"size:36"`
	CreatedAt       time.Time `json:"createdAt"`
}

// Normalized links protect every historical audio plan's media from deletion.
type DramaAudioResourceLink struct {
	VersionID  string `gorm:"size:36;primaryKey"`
	ResourceID string `gorm:"size:36;primaryKey;index"`
	ProjectID  string `gorm:"size:36;index"`
	EpisodeID  string `gorm:"size:36;index"`
}
