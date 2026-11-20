package database

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
)

func forkLedgerDatabase(t *testing.T, last int64) (*gorm.DB, time.Time) {
	t.Helper()
	db, err := Open(Config{Driver: "sqlite", DSN: "file:" + t.TempDir() + "/fork.db"})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version >= ?", 106).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	applied := time.Date(2026, 11, 18, 1, 2, 3, 0, time.UTC)
	for _, item := range schemaMigrations {
		if item.version < 110 || item.version > last+4 {
			continue
		}
		if err := db.Create(&schemaMigration{Version: item.version - 4, Name: item.name, Checksum: item.checksum, AppliedAt: applied}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, applied
}

func TestMigrateSchemaRelocatesForkLedgerBeforeUpstream(t *testing.T) {
	for _, last := range []int64{106, 108, 112} {
		t.Run(fmt.Sprint(last), func(t *testing.T) {
			db, applied := forkLedgerDatabase(t, last)
			for range 2 {
				if err := MigrateSchema(db); err != nil {
					t.Fatal(err)
				}
			}
			for _, item := range schemaMigrations {
				if item.version < 106 {
					continue
				}
				var row schemaMigration
				if err := db.First(&row, "version = ?", item.version).Error; err != nil {
					t.Fatal(err)
				}
				if err := validateMigrationRecord(row, item); err != nil {
					t.Fatal(err)
				}
				if item.version >= 110 && item.version <= last+4 && !row.AppliedAt.Equal(applied) {
					t.Fatalf("original applied time changed: %+v", row)
				}
			}
		})
	}
}

func TestMigrateSchemaRejectsUnknownForkLedgerWithoutChangingRecords(t *testing.T) {
	for _, broken := range []string{"checksum", "gap", "table"} {
		t.Run(broken, func(t *testing.T) {
			db, applied := forkLedgerDatabase(t, 112)
			switch broken {
			case "checksum":
				if err := db.Model(&schemaMigration{}).Where("version = ?", 108).Update("checksum", "unknown").Error; err != nil {
					t.Fatal(err)
				}
			case "gap":
				if err := db.Delete(&schemaMigration{}, "version = ?", 108).Error; err != nil {
					t.Fatal(err)
				}
			case "table":
				if err := db.Migrator().DropTable("drama_audio_versions"); err != nil {
					t.Fatal(err)
				}
			}
			if err := MigrateSchema(db); err == nil {
				t.Fatal("unrecognized fork ledger was accepted")
			}
			var row schemaMigration
			if err := db.First(&row, "version = ?", 106).Error; err != nil || row.Name != "external_mcp_access" || !row.AppliedAt.Equal(applied) {
				t.Fatalf("failed conversion changed source record: %+v, %v", row, err)
			}
		})
	}
}

func TestMigrateSchemaRelocatesOriginalFork102Ledger(t *testing.T) {
	db, applied := forkLedgerDatabase(t, 108)
	if err := db.Where("version >= ?", 102).Delete(&schemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range schemaMigrations {
		if item.version < 110 || item.version > 112 {
			continue
		}
		if err := db.Create(&schemaMigration{Version: item.version - 8, Name: item.name, Checksum: item.checksum, AppliedAt: applied}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := MigrateSchema(db); err != nil {
			t.Fatal(err)
		}
	}
	for version := int64(110); version <= 112; version++ {
		var row schemaMigration
		if err := db.First(&row, "version = ?", version).Error; err != nil || !row.AppliedAt.Equal(applied) {
			t.Fatalf("original fork record changed: %+v, %v", row, err)
		}
	}
}
