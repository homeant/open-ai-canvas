package database

import (
	"testing"

	"yingce/backend/internal/model"
)

func TestDramaShotMediaMigrationPreservesFormalRows(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:" + t.TempDir() + "/shot.db"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"CREATE TABLE drama_shots (id text PRIMARY KEY, dialogue text)", "INSERT INTO drama_shots (id,dialogue) VALUES ('shot','原台词')", "CREATE TABLE drama_takes (id text PRIMARY KEY, resource_id text)", "INSERT INTO drama_takes (id,resource_id) VALUES ('take','original-video')"} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	var step migration
	for _, candidate := range schemaMigrations {
		if candidate.version == 112 {
			step = candidate
		}
	}
	for range 2 {
		if err := step.apply(db); err != nil {
			t.Fatal(err)
		}
	}
	var shot model.DramaShot
	var take model.DramaTake
	if err := db.First(&shot, "id = ?", "shot").Error; err != nil || shot.Dialogue != "原台词" || shot.ImageReferencesJSON != "" {
		t.Fatalf("changed shot %+v %v", shot, err)
	}
	if err := db.First(&take, "id = ?", "take").Error; err != nil || take.ResourceID != "original-video" || take.EpisodeID != "" {
		t.Fatalf("changed take %+v %v", take, err)
	}
	if !db.Migrator().HasTable(&model.DramaShotMediaReview{}) {
		t.Fatal("missing review history")
	}
}
