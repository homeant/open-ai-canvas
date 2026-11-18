package database

import (
	"testing"
	"yingce/backend/internal/model"
)

func TestDramaMCPEditingMigrationPreservesExistingRows(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:" + t.TempDir() + "/migration.db"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE drama_shots (id text PRIMARY KEY, dialogue text)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO drama_shots (id,dialogue) VALUES ('shot','原台词')").Error; err != nil {
		t.Fatal(err)
	}
	step := schemaMigrations[len(schemaMigrations)-1]
	if step.version != 109 {
		t.Fatal("expected migration 109")
	}
	if err := step.apply(db); err != nil {
		t.Fatal(err)
	}
	if err := step.apply(db); err != nil {
		t.Fatal(err)
	}
	var shot model.DramaShot
	if err := db.First(&shot, "id = ?", "shot").Error; err != nil || shot.Dialogue != "原台词" {
		t.Fatalf("lost existing shot %+v %v", shot, err)
	}
	for _, table := range []any{&model.DramaMutationReceipt{}, &model.DramaSegment{}, &model.DramaTake{}} {
		if !db.Migrator().HasTable(table) {
			t.Fatal("missing new table")
		}
	}
}
