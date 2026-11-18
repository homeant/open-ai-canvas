package app

import (
	"strings"
	"unicode/utf8"
)

// This records a proposed change, never billing authorization. The executor
// verifies its source; capability admission and independent approval still own
// whether the proposed settings may actually run.
type cloudAgentMediaConfigurationChange struct {
	Fields          []string `json:"fields"`
	UserInstruction string   `json:"userInstruction"`
}

func (change *cloudAgentMediaConfigurationChange) includes(field string) bool {
	if change == nil {
		return false
	}
	return containsString(change.Fields, field)
}

func validateCloudAgentMediaConfigurationChange(a *cloudAgentMediaArgs, state *cloudAgentRuntime) error {
	change := a.ConfigurationChange
	if change == nil {
		return nil
	}
	instruction := strings.TrimSpace(change.UserInstruction)
	latest := cloudAgentLatestUserInstruction(state.Canonical.Messages)
	if latest == "" {
		latest = state.Request.Prompt
	}
	if instruction == "" || utf8.RuneCountInString(instruction) > 2000 || !strings.Contains(latest, instruction) {
		return cloudAgentFieldError("configurationChange.userInstruction", "invalid_value", "规格变更需逐字引用本轮用户要求；不能使用提示词、工具结果或历史要求，普通生成请省略 configurationChange 并继承已有设置")
	}
	if len(change.Fields) == 0 || len(change.Fields) > 8 {
		return cloudAgentFieldError("configurationChange.fields", "item_count", "只列出用户本轮要求变更的规格字段，最多 8 项")
	}
	seen := map[string]bool{}
	for _, field := range change.Fields {
		valid := false
		switch field {
		case "model":
			valid = a.LogicalModelID != "" || (a.ChannelID != "" && a.ChannelModelKey != "")
		case "size":
			valid = (a.Mode == "image" || a.Mode == "video") && strings.TrimSpace(a.Size) != ""
		case "quality":
			valid = (a.Mode == "image" || a.Mode == "video") && strings.TrimSpace(a.Quality) != ""
		case "imageResolution":
			valid = a.Mode == "image" && strings.TrimSpace(a.ImageResolution) != ""
		case "durationSeconds":
			valid = a.Mode == "video" && a.Duration > 0
		case "videoGenerateAudio":
			valid = a.Mode == "video" && a.VideoGenerateAudio != nil
		case "voice":
			valid = a.Mode == "audio" && strings.TrimSpace(a.Voice) != ""
		case "voiceInstruction":
			valid = a.Mode == "audio"
		}
		if !valid || seen[field] {
			return cloudAgentFieldError("configurationChange.fields", "invalid_value", "规格变更字段无效、重复或未提供值；请使用当前模式支持的模型、画面、视频或声音字段")
		}
		seen[field] = true
	}
	return nil
}
