package database

import (
	"testing"

	"yingce/backend/internal/model"
)

func TestDramaAudioMigrationPreservesVideoAndScript(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:" + t.TempDir() + "/audio.db"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.DramaSegment{}, &model.DramaShot{}); err != nil {
		t.Fatal(err)
	}
	segment := model.DramaSegment{ID: "segment", EpisodeID: "episode", Revision: 7, VideoResourceID: "original-video"}
	shot := model.DramaShot{ID: "shot", Dialogue: "原独白", DialogueMode: "inner_monologue"}
	if err := db.Create(&segment).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&shot).Error; err != nil {
		t.Fatal(err)
	}
	step := schemaMigrations[len(schemaMigrations)-1]
	if step.version != 110 {
		t.Fatal("expected audio migration")
	}
	for range 2 {
		if err := step.apply(db); err != nil {
			t.Fatal(err)
		}
	}
	var after model.DramaSegment
	if err := db.First(&after, "id = ?", segment.ID).Error; err != nil || after.Revision != 7 || after.VideoResourceID != segment.VideoResourceID {
		t.Fatalf("changed video %+v %v", after, err)
	}
	if !db.Migrator().HasTable(&model.DramaAudioResourceLink{}) {
		t.Fatal("missing deletion protection")
	}
}
