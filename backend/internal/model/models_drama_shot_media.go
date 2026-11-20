package model

import "time"

type DramaShotImageReference struct {
	ResourceID string `json:"resourceId"`
	Role       string `json:"role"`
	Name       string `json:"name"`
	CanvasID   string `json:"canvasId,omitempty"`
	NodeID     string `json:"nodeId,omitempty"`
	NodeHash   string `json:"nodeHash,omitempty"`
}

type DramaShotMediaReview struct {
	ID           string    `json:"id" gorm:"primaryKey;size:36"`
	ProjectID    string    `json:"projectId" gorm:"index;size:36"`
	EpisodeID    string    `json:"episodeId" gorm:"index;size:36"`
	ShotID       string    `json:"shotId" gorm:"size:36;index"`
	TakeID       string    `json:"takeId" gorm:"size:36;index"`
	ResourceID   string    `json:"resourceId" gorm:"size:36"`
	Sequence     int64     `json:"sequence"`
	Layer        string    `json:"layer" gorm:"size:24"`
	Status       string    `json:"status" gorm:"size:24"`
	Reason       string    `json:"reason" gorm:"type:text"`
	StartMs      int64     `json:"startMs"`
	EndMs        int64     `json:"endMs"`
	ShotRevision string    `json:"shotRevision" gorm:"size:64"`
	ReviewerID   string    `json:"reviewerId" gorm:"size:36"`
	CreatedAt    time.Time `json:"createdAt"`
}
