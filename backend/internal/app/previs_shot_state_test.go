package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math"
	"os"
	"testing"

	"yingce/backend/internal/model"
)

func TestExternalPrevisShotAnimationIsolation(t *testing.T) {
	s, db, _, _ := creationTestService(t)
	if err := db.Create(&model.CanvasProject{ID: "shot-canvas", UserID: "user", PayloadJSON: `{"nodes":[],"connections":[],"previsScenes":[]}`}).Error; err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListExternalPrevisScenes("user", ExternalPrevisScope{CanvasID: "shot-canvas"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateExternalPrevisScene("user", ExternalPrevisCreateRequest{CanvasID: "shot-canvas", CanvasSnapshotHash: listed["canvases"].([]map[string]any)[0]["canvasSnapshotHash"].(string), IdempotencyKey: "scoped-create-01", Title: "隔离动作回归", TemplateID: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	read := ExternalPrevisReadRequest{CanvasID: "shot-canvas", NodeID: created["nodeId"].(string), IncludeTransforms: true}
	current, err := s.GetExternalPrevisScene("user", read)
	if err != nil {
		t.Fatal(err)
	}
	sceneID, firstID := created["sceneId"].(string), created["shotId"].(string)
	read.SceneID = sceneID
	canvas, _ := s.repo.CanvasProjectForUser("user", "shot-canvas")
	patch := ExternalPrevisPatchRequest{CanvasID: "shot-canvas", SceneID: sceneID, SnapshotHash: current["snapshotHash"].(string), ExpectedRevision: &canvas.Revision, IdempotencyKey: "scoped-setup-01", Operations: []ExternalPrevisPatchOperation{
		{Type: "object_add", ID: "hero", Kind: "actor", Pose: "stand", Position: vec3Ptr([]float64{0, 0.95, 0}), Rotation: vec3Ptr([]float64{-math.Pi / 2, 0, 0})},
		{Type: "shot_add", ID: "second", CameraID: previsStringPtr(current["scene"].(map[string]any)["cameras"].([]map[string]any)[0]["id"].(string))},
	}}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err != nil {
		t.Fatal(err)
	}
	current, _ = s.GetExternalPrevisScene("user", read)
	canvas, _ = s.repo.CanvasProjectForUser("user", "shot-canvas")
	patch.ExpectedRevision, patch.SnapshotHash, patch.IdempotencyKey = &canvas.Revision, current["snapshotHash"].(string), "scoped-animation-01"
	patch.Operations = []ExternalPrevisPatchOperation{
		{Type: "object_keyframe_upsert", ID: "hero", ShotID: firstID, Time: float64Ptr(0), Rotation: vec3Ptr([]float64{-math.Pi / 2, 0, 0}), Pose: "stand"},
		{Type: "object_keyframe_upsert", ID: "hero", ShotID: firstID, Time: float64Ptr(5), Rotation: vec3Ptr([]float64{-math.Pi / 2, 0, 0}), Pose: "stand"},
		{Type: "object_keyframe_upsert", ID: "hero", ShotID: "second", Time: float64Ptr(5), Rotation: vec3Ptr([]float64{0, 0, 0}), Pose: "sit"},
	}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err != nil {
		t.Fatalf("old revision replay rejected: %v", err)
	}
	for _, shotID := range []string{firstID, "second"} {
		read.ShotID = shotID
		current, err = s.GetExternalPrevisScene("user", read)
		if err != nil {
			t.Fatal(err)
		}
		frames := current["scene"].(map[string]any)["shotFrames"].([]map[string]any)
		last := frames[0]["lastFrame"].(map[string]any)
		want := "stand"
		if shotID == "second" {
			want = "sit"
		}
		if last["pose"] != want || len(frames[0]["keyframes"].([]map[string]any)) != 2 {
			t.Fatalf("shot %s frames: %#v", shotID, frames)
		}
	}
	canvas, _ = s.repo.CanvasProjectForUser("user", "shot-canvas")
	var doc map[string]any
	if err := json.Unmarshal([]byte(canvas.PayloadJSON), &doc); err != nil {
		t.Fatal(err)
	}
	scene, _ := findPrevisScene(creationMaps(doc["previsScenes"]), sceneID)
	if len(creationMaps(creationMaps(scene["objects"])[0]["keyframes"])) != 0 {
		t.Fatal("shot tracks leaked into base object")
	}
	stale := patch
	stale.SnapshotHash, stale.IdempotencyKey = current["snapshotHash"].(string), "scoped-stale-revision"
	if _, err := s.ApplyExternalPrevisPatch("user", stale); err == nil {
		t.Fatal("stale revision accepted")
	}
	invalid := stale
	invalid.ExpectedRevision, invalid.IdempotencyKey = &canvas.Revision, "scoped-rollback-01"
	invalid.Operations = []ExternalPrevisPatchOperation{
		{Type: "object_update", ID: "hero", ShotID: "second", Pose: "run"},
		{Type: "object_keyframe_upsert", ID: "hero", ShotID: "second", Time: float64Ptr(6)},
	}
	if _, err := s.ApplyExternalPrevisPatch("user", invalid); err == nil {
		t.Fatal("out-of-shot time accepted")
	}
	after, _ := s.repo.CanvasProjectForUser("user", "shot-canvas")
	if after.PayloadJSON != canvas.PayloadJSON {
		t.Fatal("failed batch partially wrote")
	}
	if _, err := s.GetExternalPrevisScene("other", read); err == nil {
		t.Fatal("foreign canvas readable")
	}
	wrong := read
	wrong.NodeID = "missing-node"
	if _, err := s.GetExternalPrevisScene("user", wrong); err == nil {
		t.Fatal("missing node accepted")
	}
	if url := os.Getenv("CANVAS_PREVIS_RENDER_TEST_URL"); url != "" {
		t.Setenv("CANVAS_PREVIS_RENDER_URL", url)
		hashes := [][32]byte{}
		for index, shotID := range []string{firstID, "second"} {
			read.ShotID = shotID
			current, err = s.GetExternalPrevisScene("user", read)
			if err != nil {
				t.Fatal(err)
			}
			length, time := 320, 5.0
			frame, err := s.RenderExternalPrevis(context.Background(), "user", ExternalPrevisRenderRequest{CanvasID: "shot-canvas", SceneID: sceneID, ShotID: shotID, SnapshotHash: current["snapshotHash"].(string), IdempotencyKey: "scoped-render-" + shotID, Time: &time, LongEdge: &length, Kind: "frame"})
			if err != nil {
				t.Fatalf("render %d: %v", index, err)
			}
			_, body, err := s.OpenResource("user", frame["resourceId"].(string))
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(body)
			body.Close()
			if err != nil || len(data) < 100 {
				t.Fatalf("invalid PNG: %v", err)
			}
			hashes = append(hashes, sha256.Sum256(data))
			if dir := os.Getenv("CANVAS_PREVIS_TEST_OUTPUT_DIR"); dir != "" {
				if err := os.WriteFile(dir+"/shot-"+shotID+".png", data, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		if hashes[0] == hashes[1] {
			t.Fatal("different scoped poses produced identical frames")
		}
	}
}
