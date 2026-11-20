package app

import (
	"encoding/json"
	"math"
)

var previsShotObjectFields = []string{"visible", "transform", "pose", "keyframes", "boneOverrides", "boneTracks", "contactConstraints", "motions", "motionPath", "motionClips", "activeMotionClipId"}

func previsSceneForShot(scene map[string]any, shotID string) map[string]any {
	shot, ok := findPrevisShot(scene, shotID)
	if !ok {
		return scene
	}
	copy := map[string]any{}
	for key, value := range scene {
		copy[key] = value
	}
	objects := make([]map[string]any, 0)
	for _, object := range creationMaps(scene["objects"]) {
		objects = append(objects, previsShotObject(object, shot))
	}
	copy["objects"] = objects
	return copy
}

func previsShotObject(base, shot map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range base {
		result[key] = value
	}
	for _, state := range creationMaps(shot["objectStates"]) {
		if stringValue(state["objectId"]) != stringValue(base["id"]) {
			continue
		}
		for _, key := range previsShotObjectFields {
			if value, ok := state[key]; ok {
				result[key] = value
			}
		}
		if state["transform"] != nil && state["keyframes"] == nil {
			result["keyframes"] = []any{}
			delete(result, "motionPath")
		}
	}
	return result
}

func applyPrevisShotObjectPatch(scene map[string]any, operation cloudAgentPrevisPatchOperation, index int) (cloudAgentApprovalPreviewItem, error) {
	shot := cloudAgentPrevisFindByID(creationMaps(scene["shots"]), operation.ShotID)
	base := cloudAgentPrevisFindByID(creationMaps(scene["objects"]), operation.ID)
	if shot == nil || base == nil {
		return cloudAgentApprovalPreviewItem{}, BadAuthRequest("shotId 或对象 id 不属于当前预演场景")
	}
	switch operation.Type {
	case "object_update", "object_keyframe_upsert", "object_keyframe_remove", "object_motion_path_set", "object_motion_path_clear", "actor_motion_add", "actor_motion_remove":
	default:
		if !previsRigOperation(operation.Type) {
			return cloudAgentApprovalPreviewItem{}, BadAuthRequest("shotId 仅用于对象姿态、变换、骨骼和接触约束操作")
		}
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		return cloudAgentApprovalPreviewItem{}, err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return cloudAgentApprovalPreviewItem{}, err
	}
	allowed := map[string]bool{}
	for _, key := range []string{"type", "id", "shotId", "visible", "pose", "position", "rotation", "scale", "keyframeId", "time", "easing", "pathPoints", "pathSpeed", "startDelay", "orientToPath", "motion", "duration", "intensity"} {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] && !previsRigOperation(operation.Type) {
			return cloudAgentApprovalPreviewItem{}, BadAuthRequest("镜头内对象操作不支持字段：" + key)
		}
	}
	// Clone nested tracks so updating an existing frame cannot mutate the shared object.
	raw, err = json.Marshal(previsShotObject(base, shot))
	if err != nil {
		return cloudAgentApprovalPreviewItem{}, err
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return cloudAgentApprovalPreviewItem{}, err
	}
	if operation.Type == "object_keyframe_upsert" && operation.Time != nil && *operation.Time > 0 && len(creationMaps(object["keyframes"])) == 0 {
		id := "shot-base:" + operation.ID
		if operation.KeyframeID != nil && *operation.KeyframeID == id {
			id += "-anchor"
		}
		object["keyframes"] = []map[string]any{{"id": id, "time": 0, "transform": object["transform"], "pose": object["pose"]}}
	}
	virtual := map[string]any{"objects": []map[string]any{object}, "shots": []map[string]any{shot}, "activeShotId": operation.ShotID}
	operation.ShotID = ""
	var preview cloudAgentApprovalPreviewItem
	if previsRigOperation(operation.Type) {
		preview, err = applyPrevisRigPatch(scene, object, shot, operation)
	} else {
		preview, err = cloudAgentPrevisApplyPatchOperation(virtual, operation, index)
	}
	if err != nil {
		return cloudAgentApprovalPreviewItem{}, err
	}
	if err := validatePrevisObjectTrack(object, numberValue(shot["duration"], 5)); err != nil {
		return cloudAgentApprovalPreviewItem{}, err
	}
	state := map[string]any{"objectId": operation.ID, "motionPath": nil}
	for _, key := range previsShotObjectFields {
		if value, ok := object[key]; ok {
			state[key] = value
		}
	}
	states := make([]map[string]any, 0)
	for _, previous := range creationMaps(shot["objectStates"]) {
		if stringValue(previous["objectId"]) != operation.ID {
			states = append(states, previous)
		}
	}
	shot["objectStates"] = append(states, state)
	preview.Summary = "镜头《" + stringValue(shot["name"]) + "》：" + preview.Summary
	return preview, nil
}

func validatePrevisObjectTrack(object map[string]any, duration float64) error {
	if pose := stringValue(object["pose"]); pose != "" {
		if err := cloudAgentPrevisValidatePose(pose); err != nil {
			return err
		}
	}
	seen, previous := map[string]bool{}, -1.0
	for _, frame := range creationMaps(object["keyframes"]) {
		id, time := stringValue(frame["id"]), numberValue(frame["time"], math.NaN())
		if err := validateCloudAgentID(id, "关键帧 id", 120); err != nil {
			return err
		}
		if seen[id] || time <= previous {
			return BadAuthRequest("关键帧 ID 不可重复且时间必须递增")
		}
		seen[id], previous = true, time
		if err := cloudAgentPrevisFiniteInRange(time, "镜头内关键帧时间", 0, duration); err != nil {
			return err
		}
		if err := cloudAgentPrevisValidateTransform(frame["transform"], "镜头内关键帧"); err != nil {
			return err
		}
		if err := cloudAgentPrevisValidateEasing(stringValue(frame["easing"])); err != nil {
			return err
		}
		if pose := stringValue(frame["pose"]); pose != "" {
			if err := cloudAgentPrevisValidatePose(pose); err != nil {
				return err
			}
		}
	}
	return validatePrevisRigTracks(object, duration)
}

func previsShotEndpoint(object map[string]any, time float64, last bool) map[string]any {
	transform, pose := object["transform"], object["pose"]
	frames := creationMaps(object["keyframes"])
	if len(frames) > 0 {
		index := 0
		if last {
			index = len(frames) - 1
		}
		transform = frames[index]["transform"]
	}
	for _, frame := range frames {
		if numberValue(frame["time"], 0) <= time && stringValue(frame["pose"]) != "" {
			pose = frame["pose"]
		}
	}
	bones := map[string]any{}
	for key, value := range previsMapSafe(object["boneOverrides"]) {
		bones[key] = value
	}
	for _, track := range creationMaps(object["boneTracks"]) {
		frames := creationMaps(track["keyframes"])
		if len(frames) > 0 {
			index := 0
			if last {
				index = len(frames) - 1
			}
			bones[stringValue(track["bone"])] = frames[index]["rotation"]
		}
	}
	return map[string]any{"time": time, "transform": transform, "pose": pose, "boneRotations": bones, "visible": boolValue(object["visible"], true)}
}

func previsShotFrames(scene, shot map[string]any, selected map[string]bool) []map[string]any {
	items := make([]map[string]any, 0)
	for _, base := range creationMaps(scene["objects"]) {
		id := stringValue(base["id"])
		if len(selected) > 0 && !selected[id] {
			continue
		}
		object := previsShotObject(base, shot)
		items = append(items, map[string]any{"objectId": id, "shotId": shot["id"], "keyframes": creationMaps(object["keyframes"]), "boneTracks": creationMaps(object["boneTracks"]), "boneOverrides": object["boneOverrides"], "boneRotationSpace": "local_xyzw", "contactConstraints": creationMaps(object["contactConstraints"]), "firstFrame": previsShotEndpoint(object, 0, false), "lastFrame": previsShotEndpoint(object, numberValue(shot["duration"], 5), true)})
	}
	return items
}
