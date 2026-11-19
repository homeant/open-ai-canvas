package app

import (
	"strconv"
	"strings"

	"yingce/backend/internal/model"
)

func hasNativeVideoAudio(protocol model.ChannelInterfaceType, key string) bool {
	return protocol == model.ChannelInterfaceType("metaso-h3") && strings.HasPrefix(strings.ToLower(key), "minimax-h3")
}

// Audio output capability and an optional generation switch are distinct contracts.
func dramaSegmentGenerationConfig(request DramaSegmentRequest) map[string]any {
	config := map[string]any{"videoSeconds": itoa(request.Seconds)}
	if request.AudioOutputMode != "native" {
		config["videoGenerateAudio"] = strconv.FormatBool(request.GenerateAudio)
	}
	dramaVideoScreen{Size: request.Size, Resolution: request.Resolution}.apply(config)
	return config
}

func dramaSegmentSoundContract(request DramaSegmentRequest, needsVoice bool) map[string]any {
	mode := firstNonEmpty(request.AudioOutputMode, "unspecified")
	var supported, postDubbing any
	if mode == "native" || mode == "switch" {
		supported, postDubbing = true, false
		if mode == "switch" && needsVoice && !request.GenerateAudio {
			postDubbing = true
		}
	}
	return map[string]any{"audioOutputMode": mode, "modelSupportsAudioGeneration": supported, "generateAudioToggleSupported": mode == "switch", "speechGenerationGuaranteed": false, "hasAudioTrack": nil, "hasSpeech": nil, "usableDialogueAudio": "unverified", "needsPostDubbing": nil, "postDubbingRecommended": postDubbing, "source": "model_capability_contract", "note": "H3 原生音轨没有独立开关，generateAudio=false 不代表无声。能力声明不证明实际文件有人声、台词正确、声源或嘴部合格，须检查具体版本。"}
}
