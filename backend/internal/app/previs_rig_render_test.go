package app

import (
	"context"
	"crypto/sha256"
	"math"
	"os"
	"testing"
)

func TestExternalPrevisRigAndContactsRealFrames(t *testing.T) {
	url := os.Getenv("CANVAS_PREVIS_RENDER_TEST_URL")
	if url == "" {
		t.Skip("requires actual render page and Chrome")
	}
	t.Setenv("CANVAS_PREVIS_RENDER_URL", url)
	s, _, req := previsFeedbackFixture(t)
	read, err := s.GetExternalPrevisScene("user", req)
	if err != nil {
		t.Fatal(err)
	}
	view := read["scene"].(map[string]any)
	actor := view["objects"].([]map[string]any)[0]["id"].(string)
	camera := view["cameras"].([]map[string]any)[0]["id"].(string)
	identity := []float64{0, 0, 0, 1}
	q := []float64{math.Sin(35 * math.Pi / 360), 0, 0, math.Cos(35 * math.Pi / 360)}
	patch := ExternalPrevisPatchRequest{CanvasID: req.CanvasID, SceneID: req.SceneID, SnapshotHash: read["snapshotHash"].(string), IdempotencyKey: "rig-render-setup", Operations: []ExternalPrevisPatchOperation{
		{Type: "object_keyframe_upsert", ID: actor, ShotID: req.ShotID, Time: float64Ptr(0), Pose: "stand"},
		{Type: "object_keyframe_upsert", ID: actor, ShotID: req.ShotID, Time: float64Ptr(1.2), Pose: "reach"},
		{Type: "bone_keyframe_upsert", ID: actor, ShotID: req.ShotID, Bone: "chest", Time: float64Ptr(0), Quaternion: &identity},
		{Type: "bone_keyframe_upsert", ID: actor, ShotID: req.ShotID, Bone: "chest", Time: float64Ptr(1.2), Quaternion: &q},
		{Type: "camera_update", ID: camera, Position: vec3Ptr([]float64{1.8, 1.6, 3.2}), Target: vec3Ptr([]float64{0, 1, 0}), FocalLength: float64Ptr(50)},
	}}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err != nil {
		t.Fatal(err)
	}
	read, err = s.GetExternalPrevisScene("user", req)
	if err != nil {
		t.Fatal(err)
	}
	edge := 480
	sample := ExternalPrevisSampleRequest{CanvasID: req.CanvasID, SceneID: req.SceneID, ShotID: req.ShotID, SnapshotHash: read["snapshotHash"].(string), Times: []float64{0, 0.6, 1.2}, LongEdge: &edge}
	frames, err := s.SampleExternalPrevisFrames(context.Background(), "user", sample)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(frames.Frames[0].PNG) == sha256.Sum256(frames.Frames[1].PNG) || sha256.Sum256(frames.Frames[1].PNG) == sha256.Sum256(frames.Frames[2].PNG) {
		t.Fatal("pose transition still holds then jumps")
	}
	rigs := func(frame ExternalPrevisSampleFrame) map[string]any {
		items := creationMaps(frame.Diagnostics["rigs"])
		if len(items) != 1 {
			t.Fatalf("missing actual rig: %#v", frame.Diagnostics)
		}
		return items[0]
	}
	middle := previsMapSafe(rigs(frames.Frames[1])["boneRotations"])
	rotation, err := previsQuaternion(middle["chest"])
	if err != nil || math.Abs(rotation[0]-math.Sin(35*math.Pi/720)) > 1e-5 {
		t.Fatalf("real mid chest rotation: %v %v", rotation, err)
	}
	markers := previsMapSafe(rigs(frames.Frames[0])["contactMarkerPositions"])
	hand, ok := cloudAgentPrevisNumberVec3(markers["leftHand"])
	if !ok {
		t.Fatal("missing palm marker")
	}
	foot, ok := cloudAgentPrevisNumberVec3(markers["leftFoot"])
	if !ok {
		t.Fatal("missing sole marker")
	}
	bed := "contact-bed"
	bedPosition := []float64{hand[0] + 0.06, hand[1] + 0.05, hand[2] + 0.04}
	localTarget := []float64{0, 0.5, 0}
	groundTarget := []float64{foot[0], 0, foot[2]}
	patch.SnapshotHash = sample.SnapshotHash
	patch.IdempotencyKey = "rig-render-contacts"
	patch.Operations = []ExternalPrevisPatchOperation{
		{Type: "object_add", ID: bed, Kind: "primitive", Primitive: "box", Position: &bedPosition, Scale: vec3Ptr([]float64{0.3, 0.1, 0.3})},
		{Type: "contact_constraint_set", ID: actor, ShotID: req.ShotID, ConstraintID: "left-palm", Effector: "leftHand", Target: &localTarget, SurfaceObjectID: &bed, StartTime: float64Ptr(0), EndTime: float64Ptr(1.2), Tolerance: float64Ptr(0.03)},
		{Type: "contact_constraint_set", ID: actor, ShotID: req.ShotID, ConstraintID: "left-sole", Effector: "leftFoot", Target: &groundTarget, StartTime: float64Ptr(0), EndTime: float64Ptr(1.2), Tolerance: float64Ptr(0.03)},
	}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err != nil {
		t.Fatal(err)
	}
	read, err = s.GetExternalPrevisScene("user", req)
	if err != nil {
		t.Fatal(err)
	}
	sample.SnapshotHash = read["snapshotHash"].(string)
	before, _ := s.repo.CanvasProjectForUser("user", req.CanvasID)
	constrained, err := s.SampleExternalPrevisFrames(context.Background(), "user", sample)
	if err != nil {
		t.Fatal(err)
	}
	for index, frame := range constrained.Frames {
		contacts := creationMaps(frame.Diagnostics["contactConstraints"])
		if len(contacts) != 2 {
			t.Fatalf("missing contacts at %d", index)
		}
		for _, contact := range contacts {
			if stringValue(contact["status"]) != "satisfied" || numberValue(contact["errorMeters"], 1) > 0.03 {
				t.Fatalf("contact frame %d: %#v", index, contact)
			}
		}
		if dir := os.Getenv("CANVAS_PREVIS_TEST_OUTPUT_DIR"); dir != "" {
			name := []string{"start", "middle", "end"}[index]
			if err := os.WriteFile(dir+"/contact-"+name+".png", frame.PNG, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	after, _ := s.repo.CanvasProjectForUser("user", req.CanvasID)
	if before.PayloadJSON != after.PayloadJSON || before.Revision != after.Revision {
		t.Fatal("rendered contacts wrote canvas")
	}
}
