package app

import (
	"encoding/json"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func TestCloudAgentQwenAudioVoiceInstructionAndApproval(t *testing.T) {
	s, db, args := agentMediaFixture(t)
	capability := DefaultModelCapabilityConfigForModel(string(model.ChannelInterfaceEvoLinkQwenAudioTTS), "qwen-audio-3.1-tts-flash")
	for _, row := range []any{
		&model.ChannelModel{ID: "qwen-cm", ChannelID: "channel", ModelKey: "qwen-audio-3.1-tts-flash", DisplayName: "Qwen TTS", Capability: "audio", Protocol: model.ChannelInterfaceEvoLinkQwenAudioTTS, CapabilityConfigJSON: mustEncodeModelCapabilityConfig(t, capability), BillingMode: "fixed_request", PriceConfigured: true, Enabled: true},
		&model.ChannelModelPriceTier{ID: "qwen-tier", ChannelModelID: "qwen-cm", SelectorKey: "{}", SelectorJSON: "{}", BillingMode: "fixed_request", UnitPriceMicrocredits: 1000000, PriceConfigured: true, Enabled: true},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	args.Mode, args.Prompt, args.ChannelModelKey = "audio", "你好，今天的天气很好。", "qwen-audio-3.1-tts-flash"
	args.NodeID, args.Title, args.SourceNodeID, args.ReferenceNodeIDs = "audio-1", "试音", "", []string{}
	args.Size, args.Quality, args.Duration, args.VideoGenerateAudio = "", "", 0, nil
	args.Voice, args.VoiceInstruction = "anmingyuan_v3.1", "平静、自然地说"
	run, state := agentMediaRun(t, s, args, "request_approval")
	request, plan, err := s.prepareCloudAgentMedia(run, &state, agentMediaCall(args))
	if err != nil {
		t.Fatal(err)
	}
	config := request.Input["config"].(map[string]any)
	if config["audioVoice"] != args.Voice || config["audioInstructions"] != args.VoiceInstruction || request.Prompt != args.Prompt {
		t.Fatalf("audio inputs = %#v, prompt = %q", config, request.Prompt)
	}
	task, _, err := s.prepareCloudAgentEditedMedia(run, &state, &request, plan)
	if err != nil {
		t.Fatal(err)
	}
	var resolved map[string]any
	if err := json.Unmarshal([]byte(task.InputJSON), &resolved); err != nil {
		t.Fatal(err)
	}
	storedConfig := resolved["config"].(map[string]any)
	if storedConfig["audioVoice"] != args.Voice || storedConfig["audioInstructions"] != args.VoiceInstruction || resolved["prompt"] != args.Prompt {
		t.Fatalf("task lost Qwen voice settings: %#v", resolved)
	}
	preview, _ := json.Marshal(cloudAgentMediaApprovalPreview(plan, "Qwen TTS"))
	if !strings.Contains(string(preview), args.Voice) || !strings.Contains(string(preview), args.VoiceInstruction) {
		t.Fatalf("approval omits voice choices: %s", preview)
	}
	catalog, err := s.cloudAgentModelList(testGroupUserID(s), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(catalog)
	if !strings.Contains(string(encoded), `"audioSettings"`) || !strings.Contains(string(encoded), `"longanhuan_v3.1"`) || len(qwenAudioVoices) != 68 {
		t.Fatalf("Qwen voice catalog is incomplete: %s", encoded)
	}
	defaultVoice := args
	defaultVoice.Voice = ""
	defaultRequest, _, err := s.prepareCloudAgentMedia(run, &state, agentMediaCall(defaultVoice))
	if err != nil {
		t.Fatal(err)
	}
	if defaultRequest.Input["config"].(map[string]any)["audioVoice"] != qwenAudioDefaultVoice {
		t.Fatalf("default Qwen voice = %#v, %v", defaultRequest.Input, err)
	}
	for _, bad := range []struct {
		name string
		set  func(*cloudAgentMediaArgs)
	}{
		{"instruction", func(a *cloudAgentMediaArgs) { a.VoiceInstruction = strings.Repeat("汉", 51) }},
		{"prompt", func(a *cloudAgentMediaArgs) { a.Prompt = strings.Repeat("字", 5001) }},
		{"reference", func(a *cloudAgentMediaArgs) { a.ReferenceNodeIDs = []string{"cat"} }},
	} {
		t.Run(bad.name, func(t *testing.T) {
			invalid := args
			bad.set(&invalid)
			if _, _, err := s.prepareCloudAgentMedia(run, &state, agentMediaCall(invalid)); err == nil {
				t.Fatal("invalid Qwen request accepted")
			}
		})
	}
}

func TestCloudAgentQwenAudioTokenQuoteUsesUserGroupUSDConversion(t *testing.T) {
	s, db, args := agentMediaFixture(t)
	capability := DefaultModelCapabilityConfigForModel(string(model.ChannelInterfaceEvoLinkQwenAudioTTS), "qwen-audio-3.1-tts-flash")
	for _, row := range []any{
		&model.ChannelModel{ID: "qwen-token-cm", ChannelID: "channel", ModelKey: "qwen-audio-3.1-tts-flash", DisplayName: "Qwen TTS", Capability: "audio", Protocol: model.ChannelInterfaceEvoLinkQwenAudioTTS, CapabilityConfigJSON: mustEncodeModelCapabilityConfig(t, capability), PriceCurrency: "USD", BillingMode: "token", InputTokenPriceMicrocredits: 221_000, OutputTokenPriceMicrocredits: 1_765_000, PriceConfigured: true, Enabled: true},
		&model.ChannelModelPriceTier{ID: "qwen-token-tier", ChannelModelID: "qwen-token-cm", SelectorKey: "{}", SelectorJSON: "{}", BillingMode: "token", InputTokenPriceMicrocredits: 221_000, OutputTokenPriceMicrocredits: 1_765_000, PriceConfigured: true, Enabled: true},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	args.Mode, args.Prompt, args.ChannelModelKey = "audio", "你好，欢迎使用。", "qwen-audio-3.1-tts-flash"
	args.NodeID, args.Title, args.SourceNodeID, args.ReferenceNodeIDs = "audio-quote", "试音", "", []string{}
	args.Size, args.Quality, args.Duration, args.VideoGenerateAudio = "", "", 0, nil
	run, state := agentMediaRun(t, s, args, "request_approval")
	request, plan, err := s.prepareCloudAgentMedia(run, &state, agentMediaCall(args))
	if err != nil {
		t.Fatal(err)
	}
	_, preparation, err := s.prepareCloudAgentEditedMedia(run, &state, &request, plan)
	if err != nil {
		t.Fatal(err)
	}
	order := preparation.Order
	if order == nil || order.BillingMode != "token" || order.InputTokenPriceMicrocredits != 1_547_000 || order.OutputTokenPriceMicrocredits != 12_355_000 || order.AmountMicrocredits <= 0 {
		t.Fatalf("Qwen token quote = %#v", order)
	}
}
