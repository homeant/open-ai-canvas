package database

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestDefaultSQLiteTransactionWaitsForWriterBeforeReading(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := ConfigurePool(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var busyTimeout int
	if err := db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error; err != nil || busyTimeout != 5000 {
		t.Fatalf("busy timeout = %d, error = %v", busyTimeout, err)
	}
	if err := db.Exec("CREATE TABLE lock_test (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO lock_test (id, value) VALUES (1, 0)").Error; err != nil {
		t.Fatal(err)
	}
	writer := db.Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	defer writer.Rollback()
	if err := writer.Exec("UPDATE lock_test SET value = 1 WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- db.Transaction(func(tx *gorm.DB) error {
			var value int
			if err := tx.Raw("SELECT value FROM lock_test WHERE id = 1").Scan(&value).Error; err != nil {
				return err
			}
			return tx.Exec("UPDATE lock_test SET value = ? WHERE id = 1", value+1).Error
		})
	}()
	select {
	case err := <-result:
		t.Fatalf("second transaction returned while writer held the lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writer.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second transaction did not resume after writer commit")
	}
	var value int
	if err := db.Raw("SELECT value FROM lock_test WHERE id = 1").Scan(&value).Error; err != nil || value != 2 {
		t.Fatalf("final value = %d, error = %v", value, err)
	}
}
