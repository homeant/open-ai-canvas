package app

import (
	"encoding/json"
	"strings"
	"testing"

	"yingce/backend/internal/canvas/contract"
	"yingce/backend/internal/model"
)

func TestCloudAgentBindsQwenVoiceToExistingCharacter(t *testing.T) {
	s, db, _, _ := creationTestService(t)
	if err := db.Create(&model.CanvasProject{ID: "agent-canvas", UserID: "user", PayloadJSON: `{}`}).Error; err != nil {
		t.Fatal(err)
	}
	card := seedCloudAgentCharacter(t, db, "agent-canvas")
	voice, instruction := "anmingyuan_v3.1", "平静自然地说"
	spec := contract.GenerationSpec{
		Version: contract.GenerationVersion, Mode: "audio", Prompt: "你好。",
		ModelSelection:    &contract.ModelSelection{Kind: "channel", ChannelID: "qwen-channel", ModelKey: "qwen-audio-3.1-tts-flash"},
		Options:           contract.Options{AudioVoice: &voice, AudioInstructions: &instruction},
		ReferenceBindings: []contract.ReferenceBinding{}, TextInputMode: "prompt-only",
	}
	specRaw, _ := json.Marshal(spec)
	var specValue map[string]any
	if err := json.Unmarshal(specRaw, &specValue); err != nil {
		t.Fatal(err)
	}
	audioNode := map[string]any{"id": "trial-audio", "type": "audio", "title": "已验收试音", "metadata": map[string]any{"storageKey": "resource:trial-audio-resource", "generationSpec": specValue}}
	docRaw, _ := json.Marshal(map[string]any{"nodes": []any{card, audioNode}, "connections": []any{}})
	if err := db.Model(&model.CanvasProject{}).Where("id = ?", "agent-canvas").Update("payload_json", string(docRaw)).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range []any{
		&model.Resource{ID: "trial-audio-resource", UserID: "user", Kind: "audio", Status: model.ResourceStatusReady, MimeType: "audio/mpeg", Size: 128},
		&model.ModelChannel{ID: "qwen-channel", Name: "Qwen", Enabled: true},
		&model.ChannelModel{ID: "qwen-channel-model", ChannelID: "qwen-channel", ModelKey: "qwen-audio-3.1-tts-flash", Protocol: model.ChannelInterfaceEvoLinkQwenAudioTTS, Enabled: true},
	} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]any{"characterNodeId": "character-card", "audioNodeId": "trial-audio"}
	call := cloudAgentStoryboardCall(t, "canvas_bind_character_voice", "bind-voice", args)
	plan, err := prepareCloudAgentCharacterVoiceBind(s.repo, "user", "agent-canvas", call)
	if err != nil || plan.VoiceKey != voice || plan.Instructions != instruction {
		t.Fatalf("preview plan = %#v, %v", plan, err)
	}
	if !strings.Contains(strings.Join(plan.Preview.Items[0].Details, "\n"), voice) {
		t.Fatal("approval omitted selected voice")
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyCloudAgentCharacterVoiceBind(s.repo, "user", "agent-canvas", call, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["voiceStatus"] != "ready" {
		t.Fatalf("binding result = %#v", result)
	}
	character, err := s.Character("user", "character-asset")
	if err != nil || character.Character.Voice == nil || character.Character.Voice.Profile.VoiceKey != voice || character.Character.Voice.Instructions != instruction || character.Character.Voice.Profile.SampleResourceID != "trial-audio-resource" {
		t.Fatalf("bound character = %#v, %v", character, err)
	}
	if character.Character.Version <= 2 {
		t.Fatal("binding did not create a new character version")
	}
	meta := card["metadata"].(map[string]any)
	meta["characterVersionPolicy"] = "pinned"
	pinnedRaw, _ := json.Marshal(map[string]any{"nodes": []any{card, audioNode}, "connections": []any{}})
	if err := db.Model(&model.CanvasProject{}).Where("id = ?", "agent-canvas").Update("payload_json", string(pinnedRaw)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCloudAgentCharacterVoiceBind(s.repo, "user", "agent-canvas", call); err == nil {
		t.Fatal("pinned character version accepted")
	}
}
