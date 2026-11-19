package app

import (
	"encoding/json"
	"math"
	"sort"
)

func previsEffectiveAspectRatio(shot map[string]any) string {
	if ratio := stringValue(shot["aspectRatio"]); ratio != "" {
		return ratio
	}
	return "9:16"
}

func previsValidateShotStateFields(operation cloudAgentPrevisPatchOperation) error {
	allowed := map[string]bool{"type": true, "id": true, "objectId": true}
	if operation.Type == "shot_object_set" {
		for _, field := range []string{"visible", "position", "rotation", "scale"} {
			allowed[field] = true
		}
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	unsupported := []string{}
	for field := range fields {
		if !allowed[field] {
			unsupported = append(unsupported, field)
		}
	}
	if len(unsupported) > 0 {
		sort.Strings(unsupported)
		err := BadAuthRequest("镜头对象状态不接受这些字段；姿态请使用 shotId + object_update")
		err.Details = map[string]any{"operation": operation.Type, "unsupportedFields": unsupported, "suggestion": "object_update with shotId for pose; object_keyframe_upsert with shotId for animation"}
		return err
	}
	return nil
}

func previsAnimationWarnings(scene map[string]any, shotID string) []map[string]any {
	warnings := make([]map[string]any, 0)
	for _, shot := range creationMaps(scene["shots"]) {
		if shotID != "" && stringValue(shot["id"]) != shotID {
			continue
		}
		for _, base := range creationMaps(scene["objects"]) {
			if stringValue(base["kind"]) != "actor" && stringValue(base["primitive"]) != "character" {
				continue
			}
			object := previsShotObject(base, shot)
			frames := creationMaps(object["keyframes"])
			for index := 1; index < len(frames); index++ {
				previous, next := frames[index-1], frames[index]
				from := previsMapSafe(previous["transform"])
				to := previsMapSafe(next["transform"])
				rotationA, okA := cloudAgentPrevisNumberVec3(from["rotation"])
				rotationB, okB := cloudAgentPrevisNumberVec3(to["rotation"])
				if !okA || !okB {
					continue
				}
				tilt := math.Abs(math.Remainder(rotationB[0]-rotationA[0], 2*math.Pi)) + math.Abs(math.Remainder(rotationB[2]-rotationA[2], 2*math.Pi))
				if tilt < 0.1 {
					continue
				}
				start, end := numberValue(previous["time"], 0), numberValue(next["time"], 0)
				warnings = append(warnings, map[string]any{"code": "root_rotation_not_ik", "shotId": shot["id"], "objectId": object["id"], "startTime": start, "endTime": end, "suggestedSampleTimes": []float64{start, (start + end) / 2, end}, "message": "根节点旋转会带动整个角色，不能保证起身时手撑床面、双脚落地或不穿模；请逐帧取样。当前没有 IK 接触约束求解。"})
				if len(warnings) >= 64 {
					return append(warnings, map[string]any{"code": "warning_limit", "message": "仅返回前 64 个风险，请指定 shotId 缩小范围"})
				}
			}
		}
	}
	return warnings
}
