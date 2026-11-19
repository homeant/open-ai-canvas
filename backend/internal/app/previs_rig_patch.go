package app

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
)

var previsCanonicalBones = []string{"hips", "spine", "chest", "neck", "head", "leftShoulder", "leftUpperArm", "leftLowerArm", "leftHand", "rightShoulder", "rightUpperArm", "rightLowerArm", "rightHand", "leftUpperLeg", "leftLowerLeg", "leftFoot", "rightUpperLeg", "rightLowerLeg", "rightFoot"}
var previsRigOperations = []string{"bone_override_set", "bone_override_clear", "bone_keyframe_upsert", "bone_keyframe_remove", "bone_track_clear", "contact_constraint_set", "contact_constraint_remove"}

func previsRigOperation(value string) bool {
	for _, name := range previsRigOperations {
		if value == name {
			return true
		}
	}
	return false
}
func previsBoneSupported(value string) bool {
	for _, name := range previsCanonicalBones {
		if value == name {
			return true
		}
	}
	return false
}

func previsBoneKnown(value string) bool {
	if previsBoneSupported(value) || value == "root" {
		return true
	}
	for _, side := range []string{"left", "right"} {
		for _, finger := range []string{"Thumb", "Index", "Middle", "Ring", "Pinky"} {
			if suffix := strings.TrimPrefix(value, side+finger); suffix != value && (suffix == "1" || suffix == "2" || suffix == "3") {
				return true
			}
		}
	}
	return false
}

func previsRigSchemaProperties() map[string]any {
	return map[string]any{
		"bone":            map[string]any{"type": "string", "enum": previsCanonicalBones},
		"quaternion":      map[string]any{"type": "array", "minItems": 4, "maxItems": 4, "items": map[string]any{"type": "number"}, "description": "归一化 [x,y,z,w]，不是欧拉角"},
		"constraintId":    map[string]any{"type": "string", "maxLength": 120},
		"effector":        map[string]any{"type": "string", "enum": []string{"leftHand", "rightHand", "leftFoot", "rightFoot"}},
		"startTime":       map[string]any{"type": "number", "minimum": 0, "maximum": 60},
		"endTime":         map[string]any{"type": "number", "minimum": 0, "maximum": 60},
		"tolerance":       map[string]any{"type": "number", "minimum": 0.001, "maximum": 0.1},
		"surfaceObjectId": map[string]any{"type": "string", "maxLength": 80, "description": "有值时 target 为该物体局部点，否则为世界米制点"},
	}
}

func previsQuaternion(value any) ([]float64, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var q []float64
	if json.Unmarshal(raw, &q) != nil || len(q) != 4 {
		return nil, BadAuthRequest("quaternion 必须为 [x,y,z,w]")
	}
	norm := 0.0
	for _, v := range q {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, BadAuthRequest("四元数必须为有限数")
		}
		norm += v * v
	}
	norm = math.Sqrt(norm)
	if math.Abs(norm-1) > 0.01 {
		return nil, BadAuthRequest("四元数必须归一化，不能使用欧拉角或零四元数")
	}
	for i := range q {
		q[i] /= norm
	}
	return q, nil
}

func applyPrevisRigPatch(scene, object, shot map[string]any, op cloudAgentPrevisPatchOperation) (cloudAgentApprovalPreviewItem, error) {
	if stringValue(object["kind"]) != "actor" && stringValue(object["primitive"]) != "character" {
		return cloudAgentApprovalPreviewItem{}, BadAuthRequest("骨骼和接触约束只能用于演员")
	}
	allowed := map[string]bool{"type": true, "id": true}
	var keys []string
	switch op.Type {
	case "bone_override_set":
		keys = []string{"bone", "quaternion"}
	case "bone_override_clear", "bone_track_clear":
		keys = []string{"bone"}
	case "bone_keyframe_upsert":
		keys = []string{"bone", "quaternion", "time", "keyframeId", "easing"}
	case "bone_keyframe_remove":
		keys = []string{"bone", "keyframeId"}
	case "contact_constraint_set":
		keys = []string{"constraintId", "effector", "target", "startTime", "endTime", "tolerance", "surfaceObjectId"}
	case "contact_constraint_remove":
		keys = []string{"constraintId"}
	}
	for _, key := range keys {
		allowed[key] = true
	}
	raw, _ := json.Marshal(op)
	fields := map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return cloudAgentApprovalPreviewItem{}, err
	}
	for key := range fields {
		if !allowed[key] {
			return cloudAgentApprovalPreviewItem{}, BadAuthRequest("骨骼/约束操作不接受字段：" + key)
		}
	}
	if op.Bone != "" && !previsBoneSupported(op.Bone) {
		return cloudAgentApprovalPreviewItem{}, BadAuthRequest("不支持的规范骨骼名")
	}
	if len(op.Type) >= 5 && op.Type[:5] == "bone_" {
		if op.Bone == "" {
			return cloudAgentApprovalPreviewItem{}, BadAuthRequest("骨骼操作必须指定 bone")
		}
		overrides := previsMapSafe(object["boneOverrides"])
		switch op.Type {
		case "bone_override_set":
			q, err := previsQuaternion(op.Quaternion)
			if err != nil {
				return cloudAgentApprovalPreviewItem{}, err
			}
			overrides[op.Bone] = q
			object["boneOverrides"] = overrides
		case "bone_override_clear":
			if _, ok := overrides[op.Bone]; !ok {
				return cloudAgentApprovalPreviewItem{}, BadAuthRequest("该骨骼没有可清除的覆盖")
			}
			delete(overrides, op.Bone)
			object["boneOverrides"] = overrides
		default:
			tracks := creationMaps(object["boneTracks"])
			track := map[string]any{"bone": op.Bone, "keyframes": []any{}}
			found := false
			for _, item := range tracks {
				if stringValue(item["bone"]) == op.Bone {
					track = item
					found = true
				}
			}
			frames := creationMaps(track["keyframes"])
			if op.Type == "bone_keyframe_upsert" {
				if op.Time == nil {
					return cloudAgentApprovalPreviewItem{}, BadAuthRequest("骨骼关键帧需要 time")
				}
				if err := cloudAgentPrevisFiniteInRange(*op.Time, "骨骼关键帧时间", 0, numberValue(shot["duration"], 5)); err != nil {
					return cloudAgentApprovalPreviewItem{}, err
				}
				if err := cloudAgentPrevisValidateEasing(op.Easing); err != nil {
					return cloudAgentApprovalPreviewItem{}, err
				}
				q, err := previsQuaternion(op.Quaternion)
				if err != nil {
					return cloudAgentApprovalPreviewItem{}, err
				}
				if len(frames) == 0 && *op.Time > 0 {
					base, ok := overrides[op.Bone]
					if !ok {
						return cloudAgentApprovalPreviewItem{}, BadAuthRequest("首次非零骨骼关键帧须先明确零秒关键帧或骨骼覆盖，不能猜测初始旋转")
					}
					frames = append(frames, map[string]any{"id": "bone-base:" + op.Bone, "time": 0, "rotation": base})
				}
				id := "bone:" + op.Bone + ":" + newID()
				if op.KeyframeID != nil {
					id = *op.KeyframeID
				}
				if err := validateCloudAgentID(id, "骨骼关键帧 ID", 120); err != nil {
					return cloudAgentApprovalPreviewItem{}, err
				}
				index := -1
				for i, f := range frames {
					if stringValue(f["id"]) == id || math.Abs(numberValue(f["time"], -1)-*op.Time) < 0.001 {
						index = i
						break
					}
				}
				frame := map[string]any{"id": id, "time": *op.Time, "rotation": q, "easing": op.Easing}
				if index >= 0 {
					if op.KeyframeID == nil {
						frame["id"] = frames[index]["id"]
					}
					frames[index] = frame
				} else {
					frames = append(frames, frame)
				}
				sort.Slice(frames, func(i, j int) bool { return numberValue(frames[i]["time"], 0) < numberValue(frames[j]["time"], 0) })
			} else {
				if !found {
					return cloudAgentApprovalPreviewItem{}, BadAuthRequest("骨骼轨道不存在")
				}
				if op.Type == "bone_track_clear" {
					frames = nil
				} else {
					if op.KeyframeID == nil {
						return cloudAgentApprovalPreviewItem{}, BadAuthRequest("删除骨骼关键帧需要 keyframeId")
					}
					filtered := []map[string]any{}
					removed := false
					for _, f := range frames {
						if stringValue(f["id"]) == *op.KeyframeID {
							removed = true
						} else {
							filtered = append(filtered, f)
						}
					}
					if !removed {
						return cloudAgentApprovalPreviewItem{}, BadAuthRequest("骨骼关键帧不存在")
					}
					frames = filtered
				}
			}
			filtered := []map[string]any{}
			for _, item := range tracks {
				if stringValue(item["bone"]) != op.Bone {
					filtered = append(filtered, item)
				}
			}
			if len(frames) > 0 {
				track["keyframes"] = frames
				filtered = append(filtered, track)
			}
			object["boneTracks"] = filtered
		}
	} else {
		if err := validateCloudAgentID(op.ConstraintID, "constraintId", 120); err != nil {
			return cloudAgentApprovalPreviewItem{}, err
		}
		constraints := creationMaps(object["contactConstraints"])
		filtered := []map[string]any{}
		found := false
		for _, item := range constraints {
			if stringValue(item["id"]) == op.ConstraintID {
				found = true
			} else {
				filtered = append(filtered, item)
			}
		}
		if op.Type == "contact_constraint_remove" {
			if !found {
				return cloudAgentApprovalPreviewItem{}, BadAuthRequest("接触约束不存在")
			}
		} else {
			if !previsStandardActor(object) {
				return cloudAgentApprovalPreviewItem{}, BadAuthRequest("接触求解当前仅支持标准人偶")
			}
			if op.StartTime == nil || op.EndTime == nil || op.Target == nil {
				return cloudAgentApprovalPreviewItem{}, BadAuthRequest("接触约束需要 startTime/endTime/target")
			}
			if op.SurfaceObjectID != nil {
				if cloudAgentPrevisFindByID(creationMaps(scene["objects"]), *op.SurfaceObjectID) == nil || *op.SurfaceObjectID == op.ID {
					return cloudAgentApprovalPreviewItem{}, BadAuthRequest("接触面必须是当前场景其他对象")
				}
			}
			tolerance := 0.02
			if op.Tolerance != nil {
				tolerance = *op.Tolerance
			}
			item := map[string]any{"id": op.ConstraintID, "effector": op.Effector, "target": *op.Target, "startTime": *op.StartTime, "endTime": *op.EndTime, "tolerance": tolerance}
			if op.SurfaceObjectID != nil {
				item["surfaceObjectId"] = *op.SurfaceObjectID
			}
			filtered = append(filtered, item)
		}
		object["contactConstraints"] = filtered
	}
	return cloudAgentApprovalPreviewItem{Operation: op.Type, NodeID: op.ID, NodeTitle: stringValue(object["name"]), Summary: "修改镜头内骨骼/接触约束"}, nil
}

func previsStandardActor(object map[string]any) bool {
	url := stringValue(object["url"])
	return (stringValue(object["kind"]) == "actor" || stringValue(object["primitive"]) == "character") && stringValue(object["resourceId"]) == "" && stringValue(object["storageKey"]) == "" && stringValue(object["assetId"]) == "" && (url == "" || url == "https://cdn.jsdelivr.net/gh/mrdoob/three.js@r185/examples/models/gltf/Xbot.glb")
}

func validatePrevisRigTracks(object map[string]any, duration float64) error {
	for bone, q := range previsMapSafe(object["boneOverrides"]) {
		if !previsBoneKnown(bone) {
			return BadAuthRequest("未知骨骼覆盖")
		}
		if _, err := previsQuaternion(q); err != nil {
			return err
		}
	}
	seenBones := map[string]bool{}
	for _, track := range creationMaps(object["boneTracks"]) {
		bone := stringValue(track["bone"])
		if !previsBoneKnown(bone) || seenBones[bone] {
			return BadAuthRequest("骨骼轨道无效或重复")
		}
		seenBones[bone] = true
		frames := creationMaps(track["keyframes"])
		if len(frames) > 128 {
			return BadAuthRequest("每骨骼最多128个关键帧")
		}
		previous := -1.0
		ids := map[string]bool{}
		for _, frame := range frames {
			id := stringValue(frame["id"])
			at := numberValue(frame["time"], math.NaN())
			if err := validateCloudAgentID(id, "骨骼关键帧 ID", 120); err != nil {
				return err
			}
			if ids[id] || at <= previous {
				return BadAuthRequest("骨骼关键帧 ID 重复或时间未递增")
			}
			ids[id] = true
			previous = at
			if err := cloudAgentPrevisFiniteInRange(at, "骨骼关键帧时间", 0, duration); err != nil {
				return err
			}
			if _, err := previsQuaternion(frame["rotation"]); err != nil {
				return err
			}
			if err := cloudAgentPrevisValidateEasing(stringValue(frame["easing"])); err != nil {
				return err
			}
		}
	}
	constraints := creationMaps(object["contactConstraints"])
	if len(constraints) > 8 {
		return BadAuthRequest("每演员最多8个接触约束")
	}
	if len(constraints) > 0 && !previsStandardActor(object) {
		return BadAuthRequest("接触约束仅支持标准人偶")
	}
	if len(constraints) > 0 {
		scale, ok := cloudAgentPrevisNumberVec3(previsMapSafe(object["transform"])["scale"])
		if !ok || scale[0] <= 0 || math.Abs(scale[0]-scale[1]) > 1e-5 || math.Abs(scale[0]-scale[2]) > 1e-5 {
			return BadAuthRequest("接触约束要求演员使用正值等比例缩放")
		}
	}
	ids := map[string]bool{}
	for i, c := range constraints {
		id := stringValue(c["id"])
		if err := validateCloudAgentID(id, "constraintId", 120); err != nil {
			return err
		}
		if ids[id] {
			return BadAuthRequest("接触约束 ID 重复")
		}
		ids[id] = true
		effector := stringValue(c["effector"])
		if err := cloudAgentPrevisValidateEnum(effector, "effector", "leftHand", "rightHand", "leftFoot", "rightFoot"); err != nil {
			return err
		}
		start, end := numberValue(c["startTime"], math.NaN()), numberValue(c["endTime"], math.NaN())
		if err := cloudAgentPrevisFiniteInRange(start, "接触开始时间", 0, duration); err != nil {
			return err
		}
		if err := cloudAgentPrevisFiniteInRange(end, "接触结束时间", start+0.001, duration); err != nil {
			return err
		}
		if err := cloudAgentPrevisFiniteInRange(numberValue(c["tolerance"], math.NaN()), "接触容差", 0.001, 0.1); err != nil {
			return err
		}
		target, ok := cloudAgentPrevisNumberVec3(c["target"])
		if !ok {
			return BadAuthRequest("接触 target 必须为米制三维点")
		}
		if err := cloudAgentPrevisValidateVec3(target, "接触点"); err != nil {
			return err
		}
		for _, other := range constraints[:i] {
			if stringValue(other["effector"]) == effector && math.Max(start, numberValue(other["startTime"], 0)) <= math.Min(end, numberValue(other["endTime"], 0)) {
				return BadAuthRequest("同一末端的接触区间不可重叠")
			}
		}
	}
	return nil
}
