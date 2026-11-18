package model

import "time"

// DramaMutationReceipt commits with its mutation so retries return the original result.
type DramaMutationReceipt struct {
	ProjectID   string `gorm:"index;size:36"`
	EpisodeID   string `gorm:"index;size:36"`
	ID          string `gorm:"primaryKey;size:64"`
	RequestHash string `gorm:"size:64"`
	ResultJSON  string `gorm:"type:text"`
	CreatedAt   time.Time
}
