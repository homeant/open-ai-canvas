package repository

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"yingce/backend/internal/model"
)

func TestSQLiteClaimWaitsForConcurrentWriter(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "claim.db") + "?_journal_mode=WAL&_busy_timeout=5000"
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	writer, claimant := open(), open()
	if err := writer.AutoMigrate(&model.Task{}); err != nil {
		t.Fatal(err)
	}
	for _, task := range []model.Task{
		{ID: "queued", UserID: "user", Status: model.TaskStatusQueued},
		{ID: "blocker", UserID: "user", Status: model.TaskStatusSucceeded},
	} {
		if err := writer.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
	}
	tx := writer.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Model(&model.Task{}).Where("id = ?", "blocker").Update("stage", "writing").Error; err != nil {
		t.Fatal(err)
	}
	type claimResult struct {
		task *model.Task
		err  error
	}
	result := make(chan claimResult, 1)
	go func() {
		task, err := New(claimant).ClaimNextTask("worker", time.Minute)
		result <- claimResult{task, err}
	}()
	select {
	case claimed := <-result:
		t.Fatalf("claim returned while writer held the lock: task=%v err=%v", claimed.task, claimed.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case claimed := <-result:
		if claimed.err != nil || claimed.task == nil || claimed.task.ID != "queued" || claimed.task.LeaseOwner != "worker" {
			t.Fatalf("claim after writer commit: task=%+v err=%v", claimed.task, claimed.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("claim did not resume after writer commit")
	}
}
