package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math"
	"os"
	"testing"

	"gorm.io/gorm"
	"yingce/backend/internal/model"
)

func previsFeedbackFixture(t *testing.T) (*Service, *gorm.DB, ExternalPrevisReadRequest) {
	t.Helper()
	s, db, _, _ := creationTestService(t)
	scene, err := cloudAgentPrevisSceneCreateTemplate(cloudAgentPrevisSceneCreateArgs{SceneID: "feedback-scene", Title: "动作反馈回归", TemplateID: "monologue"})
	if err != nil {
		t.Fatal(err)
	}
	delete(creationMaps(scene["shots"])[0], "aspectRatio")
	raw, err := json.Marshal(map[string]any{"nodes": []any{}, "connections": []any{}, "previsScenes": []any{scene}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CanvasProject{ID: "feedback-canvas", UserID: "user", PayloadJSON: string(raw), Revision: 7}).Error; err != nil {
		t.Fatal(err)
	}
	return s, db, ExternalPrevisReadRequest{CanvasID: "feedback-canvas", SceneID: "feedback-scene", ShotID: stringValue(creationMaps(scene["shots"])[0]["id"])}
}

func TestExternalPrevisDefaultAspectAndRejectedFields(t *testing.T) {
	s, _, req := previsFeedbackFixture(t)
	before, _ := s.repo.CanvasProjectForUser("user", req.CanvasID)
	read, err := s.GetExternalPrevisScene("user", req)
	if err != nil {
		t.Fatal(err)
	}
	shot := read["scene"].(map[string]any)["shots"].([]map[string]any)[0]
	if shot["aspectRatio"] != "9:16" || shot["aspectRatioDefaulted"] != true {
		t.Fatalf("effective ratio: %#v", shot)
	}
	validation, err := s.ValidateExternalPrevisScene("user", req)
	if err != nil || validation["valid"] != true || validation["motionValidated"] != false {
		t.Fatalf("validation: %#v %v", validation, err)
	}
	actor := read["scene"].(map[string]any)["objects"].([]map[string]any)[0]["id"].(string)
	visible := true
	_, err = s.ApplyExternalPrevisPatch("user", ExternalPrevisPatchRequest{CanvasID: req.CanvasID, SceneID: req.SceneID, SnapshotHash: read["snapshotHash"].(string), IdempotencyKey: "unsupported-pose", Operations: []ExternalPrevisPatchOperation{{Type: "shot_object_set", ID: req.ShotID, ObjectID: &actor, Visible: &visible, Pose: "sit"}}})
	appErr, ok := err.(*AppError)
	if !ok || len(appErr.Details["unsupportedFields"].([]string)) != 1 || appErr.Details["unsupportedFields"].([]string)[0] != "pose" {
		t.Fatalf("unsupported pose was ignored: %v", err)
	}
	after, _ := s.repo.CanvasProjectForUser("user", req.CanvasID)
	if before.PayloadJSON != after.PayloadJSON || before.Revision != after.Revision {
		t.Fatal("read/validate/rejected patch wrote canvas")
	}
}

func TestExternalPrevisShotStatePreservesAnimation(t *testing.T) {
	s, _, req := previsFeedbackFixture(t)
	canvas, _ := s.repo.CanvasProjectForUser("user", req.CanvasID)
	doc, _ := creationDocument(canvas.PayloadJSON)
	scene, _ := findPrevisScene(creationMaps(doc["previsScenes"]), req.SceneID)
	actor := stringValue(creationMaps(scene["objects"])[0]["id"])
	for _, op := range []ExternalPrevisPatchOperation{
		{Type: "object_keyframe_upsert", ID: actor, ShotID: req.ShotID, Time: float64Ptr(0), Rotation: vec3Ptr([]float64{-math.Pi / 2, 0, 0})},
		{Type: "object_keyframe_upsert", ID: actor, ShotID: req.ShotID, Time: float64Ptr(5), Rotation: vec3Ptr([]float64{0, 0, 0}), Pose: "sit"},
	} {
		if _, err := cloudAgentPrevisApplyPatchOperation(scene, op, 0); err != nil {
			t.Fatal(err)
		}
	}
	visible := false
	if _, err := cloudAgentPrevisApplyPatchOperation(scene, ExternalPrevisPatchOperation{Type: "shot_object_set", ID: req.ShotID, ObjectID: &actor, Visible: &visible}, 0); err != nil {
		t.Fatal(err)
	}
	shot, _ := findPrevisShot(scene, req.ShotID)
	if len(creationMaps(previsShotObject(creationMaps(scene["objects"])[0], shot)["keyframes"])) != 2 {
		t.Fatal("visibility edit erased animation")
	}
	if len(previsAnimationWarnings(scene, req.ShotID)) != 1 {
		t.Fatal("root rotation was treated as validated IK")
	}
}

func TestExternalPrevisSampleFramesIntegration(t *testing.T) {
	url := os.Getenv("CANVAS_PREVIS_RENDER_TEST_URL")
	if url == "" {
		t.Skip("requires local Chrome and the actual render page")
	}
	t.Setenv("CANVAS_PREVIS_RENDER_URL", url)
	s, db, readReq := previsFeedbackFixture(t)
	read, err := s.GetExternalPrevisScene("user", readReq)
	if err != nil {
		t.Fatal(err)
	}
	actor := read["scene"].(map[string]any)["objects"].([]map[string]any)[0]["id"].(string)
	_, err = s.ApplyExternalPrevisPatch("user", ExternalPrevisPatchRequest{CanvasID: readReq.CanvasID, SceneID: readReq.SceneID, SnapshotHash: read["snapshotHash"].(string), IdempotencyKey: "sample-motion-01", Operations: []ExternalPrevisPatchOperation{
		{Type: "object_keyframe_upsert", ID: actor, ShotID: readReq.ShotID, Time: float64Ptr(0), Rotation: vec3Ptr([]float64{-math.Pi / 2, 0, 0})},
		{Type: "object_keyframe_upsert", ID: actor, ShotID: readReq.ShotID, Time: float64Ptr(1), Rotation: vec3Ptr([]float64{0, 0, 0}), Pose: "sit"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	read, err = s.GetExternalPrevisScene("user", readReq)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.repo.CanvasProjectForUser("user", readReq.CanvasID)
	var resourcesBefore, resourcesAfter, receiptsBefore, receiptsAfter int64
	db.Model(&model.Resource{}).Count(&resourcesBefore)
	db.Model(&model.CanvasMutationReceipt{}).Count(&receiptsBefore)
	req := ExternalPrevisSampleRequest{CanvasID: readReq.CanvasID, SceneID: readReq.SceneID, ShotID: readReq.ShotID, SnapshotHash: read["snapshotHash"].(string), Times: []float64{0, 0.5, 1}, LongEdge: func() *int { v := 320; return &v }()}
	result, err := s.SampleExternalPrevisFrames(context.Background(), "user", req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ReadOnly || len(result.Frames) != 3 {
		t.Fatalf("sample frames: %#v", result)
	}
	for index, frame := range result.Frames {
		if frame.Time != req.Times[index] || len(frame.PNG) < 100 || frame.Diagnostics["method"] != "rendered_world_aabb" || frame.Diagnostics["ikConstraintsSupported"] != true || frame.Diagnostics["fullBodyIK"] != false {
			t.Fatalf("frame %d: invalid image or diagnostics", index)
		}
	}
	if len(result.Warnings) == 0 || sha256.Sum256(result.Frames[0].PNG) == sha256.Sum256(result.Frames[2].PNG) {
		t.Fatal("time sampling did not evaluate the pose transition")
	}
	after, _ := s.repo.CanvasProjectForUser("user", readReq.CanvasID)
	db.Model(&model.Resource{}).Count(&resourcesAfter)
	db.Model(&model.CanvasMutationReceipt{}).Count(&receiptsAfter)
	if resourcesBefore != resourcesAfter || receiptsBefore != receiptsAfter {
		t.Fatal("read-only samples created resources or mutation receipts")
	}
	if before.PayloadJSON != after.PayloadJSON || before.Revision != after.Revision {
		t.Fatal("read-only samples changed canvas")
	}
	if _, err := s.SampleExternalPrevisFrames(context.Background(), "other", req); err == nil {
		t.Fatal("foreign canvas sampled")
	}
	for _, times := range [][]float64{{1, 0}, {6}, {math.NaN()}, {0, 0}, {0, 1, 2, 3, 4, 5, 6, 7, 8}} {
		bad := req
		bad.Times = times
		if _, err := s.SampleExternalPrevisFrames(context.Background(), "user", bad); err == nil {
			t.Fatalf("invalid times accepted: %v", times)
		}
	}
}
