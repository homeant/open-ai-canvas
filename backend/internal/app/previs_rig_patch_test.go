package app

import (
	"math"
	"testing"
)

func TestExternalPrevisBoneTracksScopedReplayAndRollback(t *testing.T) {
	s, _, req := previsFeedbackFixture(t)
	read, err := s.GetExternalPrevisScene("user", req)
	if err != nil {
		t.Fatal(err)
	}
	scene := read["scene"].(map[string]any)
	actor := scene["objects"].([]map[string]any)[0]["id"].(string)
	camera := scene["cameras"].([]map[string]any)[0]["id"].(string)
	q := []float64{math.Sin(35 * math.Pi / 360), 0, 0, math.Cos(35 * math.Pi / 360)}
	identity := []float64{0, 0, 0, 1}
	patch := ExternalPrevisPatchRequest{CanvasID: req.CanvasID, SceneID: req.SceneID, SnapshotHash: read["snapshotHash"].(string), IdempotencyKey: "rig-setup-01", Operations: []ExternalPrevisPatchOperation{
		{Type: "shot_add", ID: "rig-second", CameraID: &camera},
		{Type: "bone_override_set", ID: actor, ShotID: req.ShotID, Bone: "chest", Quaternion: &q},
		{Type: "bone_keyframe_upsert", ID: actor, ShotID: "rig-second", Bone: "chest", Time: float64Ptr(0), Quaternion: &identity, KeyframeID: previsStringPtr("chest-start")},
		{Type: "bone_keyframe_upsert", ID: actor, ShotID: "rig-second", Bone: "chest", Time: float64Ptr(1.2), Quaternion: &q, KeyframeID: previsStringPtr("chest-end")},
	}}
	result, err := s.ApplyExternalPrevisPatch("user", patch)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ApplyExternalPrevisPatch("user", patch)
	if err != nil || replay["snapshotHash"] != result["snapshotHash"] {
		t.Fatalf("replay: %v %v", replay, err)
	}
	first, err := s.GetExternalPrevisScene("user", req)
	if err != nil {
		t.Fatal(err)
	}
	firstFrames := first["scene"].(map[string]any)["shotFrames"].([]map[string]any)
	got, err := previsQuaternion(firstFrames[0]["boneOverrides"].(map[string]any)["chest"])
	if err != nil || math.Abs(got[0]-q[0]) > 1e-9 {
		t.Fatalf("first override: %v %v", got, err)
	}
	req.ShotID = "rig-second"
	second, err := s.GetExternalPrevisScene("user", req)
	if err != nil {
		t.Fatal(err)
	}
	frames := second["scene"].(map[string]any)["shotFrames"].([]map[string]any)
	if len(previsMapSafe(frames[0]["boneOverrides"])) != 0 || len(creationMaps(frames[0]["boneTracks"])) != 1 {
		t.Fatal("bone state leaked between shots")
	}
	end := previsMapSafe(previsMapSafe(frames[0]["lastFrame"])["boneRotations"])
	if _, err := previsQuaternion(end["chest"]); err != nil {
		t.Fatal("bone endpoint omitted")
	}
	before, _ := s.repo.CanvasProjectForUser("user", req.CanvasID)
	doc, _ := creationDocument(before.PayloadJSON)
	rawScene, _ := findPrevisScene(creationMaps(doc["previsScenes"]), req.SceneID)
	if len(previsMapSafe(creationMaps(rawScene["objects"])[0]["boneOverrides"])) != 0 {
		t.Fatal("shot override changed shared actor")
	}
	for index, invalid := range []ExternalPrevisPatchOperation{
		{Type: "bone_override_set", ID: actor, Bone: "chest", Quaternion: &q},
		{Type: "bone_override_set", ID: actor, ShotID: req.ShotID, Bone: "invalid", Quaternion: &q},
		{Type: "bone_override_set", ID: actor, ShotID: req.ShotID, Bone: "chest", Quaternion: vec3Ptr([]float64{1, 0, 0})},
		{Type: "bone_keyframe_upsert", ID: actor, ShotID: req.ShotID, Bone: "leftHand", Time: float64Ptr(1), Quaternion: &identity},
		{Type: "bone_keyframe_upsert", ID: actor, ShotID: req.ShotID, Bone: "chest", Time: float64Ptr(6), Quaternion: &q},
	} {
		patch.SnapshotHash = second["snapshotHash"].(string)
		patch.IdempotencyKey = "invalid-rig-" + string(rune('a'+index))
		patch.Operations = []ExternalPrevisPatchOperation{{Type: "object_update", ID: actor, ShotID: req.ShotID, Pose: "sit"}, invalid}
		if _, err := s.ApplyExternalPrevisPatch("user", patch); err == nil {
			t.Fatalf("invalid rig op accepted: %+v", invalid)
		}
		after, _ := s.repo.CanvasProjectForUser("user", req.CanvasID)
		if after.PayloadJSON != before.PayloadJSON || after.Revision != before.Revision {
			t.Fatal("bad rig batch partly committed")
		}
	}
}

func TestExternalPrevisContactReferencesAndIntervals(t *testing.T) {
	s, _, req := previsFeedbackFixture(t)
	read, err := s.GetExternalPrevisScene("user", req)
	if err != nil {
		t.Fatal(err)
	}
	actor := read["scene"].(map[string]any)["objects"].([]map[string]any)[0]["id"].(string)
	bed := "bed"
	target := []float64{0, 0.5, 0}
	patch := ExternalPrevisPatchRequest{CanvasID: req.CanvasID, SceneID: req.SceneID, SnapshotHash: read["snapshotHash"].(string), IdempotencyKey: "contact-setup-01", Operations: []ExternalPrevisPatchOperation{
		{Type: "object_add", ID: bed, Kind: "primitive", Primitive: "box"},
		{Type: "contact_constraint_set", ID: actor, ShotID: req.ShotID, ConstraintID: "palm", Effector: "leftHand", SurfaceObjectID: &bed, Target: &target, StartTime: float64Ptr(0), EndTime: float64Ptr(1.2)},
	}}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetExternalPrevisScene("user", req)
	if err != nil {
		t.Fatal(err)
	}
	frames := current["scene"].(map[string]any)["shotFrames"].([]map[string]any)
	if len(creationMaps(frames[0]["contactConstraints"])) != 1 {
		t.Fatal("contact was not readable")
	}
	before, _ := s.repo.CanvasProjectForUser("user", req.CanvasID)
	patch.SnapshotHash = current["snapshotHash"].(string)
	patch.IdempotencyKey = "contact-delete-bed"
	patch.Operations = []ExternalPrevisPatchOperation{{Type: "object_remove", ID: bed}}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err == nil {
		t.Fatal("referenced contact surface was deleted")
	}
	patch.IdempotencyKey = "contact-overlap-01"
	patch.Operations = []ExternalPrevisPatchOperation{{Type: "contact_constraint_set", ID: actor, ShotID: req.ShotID, ConstraintID: "conflict", Effector: "leftHand", Target: &target, StartTime: float64Ptr(0.5), EndTime: float64Ptr(2)}}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err == nil {
		t.Fatal("overlapping contact accepted")
	}
	after, _ := s.repo.CanvasProjectForUser("user", req.CanvasID)
	if before.PayloadJSON != after.PayloadJSON {
		t.Fatal("invalid contact wrote scene")
	}
	patch.IdempotencyKey = "contact-remove-01"
	patch.Operations = []ExternalPrevisPatchOperation{{Type: "contact_constraint_remove", ID: actor, ShotID: req.ShotID, ConstraintID: "palm"}, {Type: "object_remove", ID: bed}}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err != nil {
		t.Fatal(err)
	}
}
