package app

import (
	"encoding/json"
	"testing"

	"yingce/backend/internal/model"
)

func TestDramaH3NativeAudioDoesNotSendUnsupportedSwitch(t *testing.T) {
	s, fake := newDramaTestService(t)
	video := dramaVideoFixture()
	video.GenerateAudio = VideoBooleanConfig{Supported: false, Default: false}
	capability, _ := json.Marshal(&ModelCapabilityConfig{Version: 1, Video: &video})
	row := model.ChannelModel{ID: "native-h3", ChannelID: "ch", ModelKey: "MiniMax-H3", Capability: "video", Protocol: model.ChannelInterfaceType("metaso-h3"), Enabled: true, CapabilityConfigJSON: string(capability)}
	if err := fake.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	choice := DramaModelChoice{ChannelID: row.ChannelID, ModelKey: row.ModelKey}
	profile, err := s.dramaVideoProfile(choice)
	if err != nil || profile.GenerateAudio || profile.AudioOutputMode != "native" {
		t.Fatalf("profile %+v %v", profile, err)
	}
	request := DramaSegmentRequest{Seconds: 4, Size: "9:16", Resolution: "768p", GenerateAudio: profile.GenerateAudio, AudioOutputMode: profile.AudioOutputMode}
	config := dramaSegmentGenerationConfig(request)
	if _, exists := config["videoGenerateAudio"]; exists {
		t.Fatal("H3 has no audio switch")
	}
	if config["videoSeconds"] != "4" || config["size"] != "9:16" || config["vquality"] != "768p" {
		t.Fatalf("lost video specs %+v", config)
	}
	sound := dramaSegmentSoundContract(request, true)
	if sound["modelSupportsAudioGeneration"] != true || sound["generateAudioToggleSupported"] != false || sound["postDubbingRecommended"] != false || sound["hasSpeech"] != nil || sound["hasAudioTrack"] != nil {
		t.Fatalf("native audio misrepresented %+v", sound)
	}
	option := s.dramaModelOption(choice, "video", "H3", "")
	if option.GenerateAudio == nil || !*option.GenerateAudio || option.AudioOutputMode != "native" {
		t.Fatalf("catalog implies silent H3 %+v", option)
	}
}

func TestDramaSoundContractKeepsUnknownOutputUnknown(t *testing.T) {
	for _, mode := range []string{"native", "switch", "unspecified"} {
		r := DramaSegmentRequest{Seconds: 4, AudioOutputMode: mode, GenerateAudio: mode == "switch"}
		got := dramaSegmentSoundContract(r, true)
		if got["hasAudioTrack"] != nil || got["hasSpeech"] != nil || got["speechGenerationGuaranteed"] != false {
			t.Fatalf("capability became QA %+v", got)
		}
		if mode == "unspecified" && (got["modelSupportsAudioGeneration"] != nil || got["postDubbingRecommended"] != nil) {
			t.Fatalf("missing switch implies silence %+v", got)
		}
		if mode == "switch" && dramaSegmentGenerationConfig(r)["videoGenerateAudio"] != "true" {
			t.Fatal("lost supported audio switch")
		}
	}
}
