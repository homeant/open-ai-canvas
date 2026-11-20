package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"yingce/backend/internal/canvas/contract"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

const qwenAgentVoiceProvider = "evolink_qwen_audio_tts"

type cloudAgentCharacterVoiceBindArgs struct {
	CharacterNodeID string `json:"characterNodeId"`
	AudioNodeID     string `json:"audioNodeId"`
}

type cloudAgentCharacterVoiceBindPlan struct {
	Args            cloudAgentCharacterVoiceBindArgs
	AssetID         string
	AudioResourceID string
	VoiceName       string
	VoiceKey        string
	Instructions    string
	Preview         cloudAgentApprovalPreview
}

func cloudAgentQwenSystemVoice(id string) (string, bool) {
	for _, voice := range qwenAudioVoices {
		if voice.ID == id {
			return voice.Label, true
		}
	}
	return "", false
}

func cloudAgentQwenAudioNodeSettings(repo *repository.Repository, audioNode map[string]any) (string, string, error) {
	meta, _ := audioNode["metadata"].(map[string]any)
	raw, ok := meta["generationSpec"]
	if !ok {
		return "", "", nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return "", "", err
	}
	spec, err := contract.Decode(data)
	if err != nil || spec.Mode != "audio" || spec.ModelSelection == nil || spec.ModelSelection.Kind != "channel" {
		return "", "", nil
	}
	selected, err := repo.ChannelModelByKeyIncludingDisabled(spec.ModelSelection.ChannelID, spec.ModelSelection.ModelKey)
	if err != nil {
		return "", "", err
	}
	if selected.Protocol != model.ChannelInterfaceEvoLinkQwenAudioTTS {
		return "", "", nil
	}
	voice := qwenAudioDefaultVoice
	if spec.Options.AudioVoice != nil && strings.TrimSpace(*spec.Options.AudioVoice) != "" {
		voice = strings.TrimSpace(*spec.Options.AudioVoice)
	}
	if _, ok := cloudAgentQwenSystemVoice(voice); !ok {
		return "", "", BadAuthRequest("自定义 Qwen 音色 ID 会过期，不能作为可复用的角色声音绑定；请选择系统音色")
	}
	instructions := ""
	if spec.Options.AudioInstructions != nil {
		instructions = strings.TrimSpace(*spec.Options.AudioInstructions)
	}
	return voice, instructions, nil
}

func prepareCloudAgentCharacterVoiceBind(repo *repository.Repository, userID, canvasID string, call cloudAgentCall) (*cloudAgentCharacterVoiceBindPlan, error) {
	var args cloudAgentCharacterVoiceBindArgs
	if err := decodeCloudAgentJSONObject(call.Function.Arguments, &args); err != nil {
		return nil, cloudAgentJSONArgumentError(err)
	}
	if err := validateCloudAgentID(args.CharacterNodeID, "角色卡节点ID", 80); err != nil {
		return nil, &cloudAgentArgumentError{err}
	}
	if err := validateCloudAgentID(args.AudioNodeID, "音频节点ID", 80); err != nil {
		return nil, &cloudAgentArgumentError{err}
	}
	canvas, err := repo.CanvasProjectForUser(userID, canvasID)
	if err != nil {
		return nil, err
	}
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		return nil, err
	}
	nodes, err := creationObjects(doc["nodes"])
	if err != nil {
		return nil, err
	}
	characterNode := nodes[args.CharacterNodeID]
	if characterNode == nil || !cloudAgentCharacterNode(characterNode) {
		return nil, BadAuthRequest("角色卡节点不在当前画布")
	}
	character, err := cloudAgentResolveCharacter(repo, userID, canvas.ProjectID, characterNode)
	if err != nil {
		return nil, err
	}
	if character.VersionPolicy != "current" {
		return nil, BadAuthRequest("角色卡固定在旧版本，请先切换到当前版本再绑定声音")
	}
	resourceID, audioNode, err := cloudAgentCharacterMediaResource(repo, userID, nodes, args.AudioNodeID, "audio", "声音音频")
	if err != nil {
		return nil, err
	}
	voiceKey, instructions, err := cloudAgentQwenAudioNodeSettings(repo, audioNode)
	if err != nil {
		return nil, err
	}
	voiceName := truncateRunes(strings.TrimSpace(stringValue(audioNode["title"])), 160)
	if voiceKey != "" {
		voiceName, _ = cloudAgentQwenSystemVoice(voiceKey)
	}
	details := []string{"试听音频：《" + cloudAgentApprovalNodeTitle(audioNode, "音频") + "》"}
	if voiceKey != "" {
		details = append(details, "Qwen 音色："+voiceName+" ("+voiceKey+")")
		if instructions != "" {
			details = append(details, "声音指令："+instructions)
		}
	}
	return &cloudAgentCharacterVoiceBindPlan{
		Args: args, AssetID: character.AssetID, AudioResourceID: resourceID,
		VoiceName: voiceName, VoiceKey: voiceKey, Instructions: instructions,
		Preview: cloudAgentApprovalPreview{
			Kind: "canvas_mutation", Title: "确认绑定角色声音",
			Description: fmt.Sprintf("把已就绪音频绑定到角色卡《%s》，并创建新的角色资产版本。批准后才会执行。", character.Name),
			Items:       []cloudAgentApprovalPreviewItem{{Operation: "bind_character_voice", NodeID: args.CharacterNodeID, NodeTitle: character.Name, NodeType: "character", NodeTypeLabel: "角色卡", Details: details, Summary: fmt.Sprintf("绑定《%s》的声音", character.Name)}},
		},
	}, nil
}

func cloudAgentPreviewCharacterVoiceBind(repo *repository.Repository, userID, canvasID string, call cloudAgentCall) (cloudAgentApprovalPreview, error) {
	plan, err := prepareCloudAgentCharacterVoiceBind(repo, userID, canvasID, call)
	if err != nil {
		return cloudAgentApprovalPreview{}, err
	}
	return plan.Preview, nil
}

func cloudAgentQwenVoiceProfile(repo *repository.Repository, userID string, plan *cloudAgentCharacterVoiceBindPlan) (string, error) {
	profile, err := repo.VoiceProfileByProviderKey(userID, qwenAgentVoiceProvider, plan.VoiceKey)
	if err == nil {
		if profile.Status != "active" {
			return "", BadAuthRequest("选定的 Qwen 音色档案不可用")
		}
		return profile.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	now := time.Now()
	profile = &model.VoiceProfile{
		ID: newID(), UserID: userID, Name: plan.VoiceName, Provider: qwenAgentVoiceProvider,
		VoiceKey: plan.VoiceKey, Language: "按模型支持", Timbre: "Qwen 系统音色",
		SampleResourceID: plan.AudioResourceID, CompatibleModelsJSON: `["qwen-audio-3.1-tts-flash"]`,
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.CreateVoiceProfile(profile); err != nil {
		return "", err
	}
	return profile.ID, nil
}

func applyCloudAgentCharacterVoiceBind(repo *repository.Repository, userID, canvasID string, call cloudAgentCall, policy RuntimePolicySetting, recorder ...cloudAgentMutationRecorder) (any, error) {
	plan, err := prepareCloudAgentCharacterVoiceBind(repo, userID, canvasID, call)
	if err != nil {
		return nil, err
	}
	request := BindCharacterVoiceRequest{SampleResourceID: plan.AudioResourceID, VoiceName: plan.VoiceName, Instructions: plan.Instructions}
	if plan.VoiceKey != "" {
		request.VoiceProfileID, err = cloudAgentQwenVoiceProfile(repo, userID, plan)
		if err != nil {
			return nil, err
		}
		request.SampleResourceID = ""
	}
	bound, err := (&Service{repo: repo}).BindCharacterVoice(userID, plan.AssetID, request)
	if err != nil {
		return nil, err
	}
	canvas, err := repo.CanvasProjectForUser(userID, canvasID)
	if err != nil {
		return nil, err
	}
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		return nil, err
	}
	beforeJSON, beforeHash := canvas.PayloadJSON, cloudAgentCanvasHash(doc)
	nodes, err := creationObjects(doc["nodes"])
	if err != nil {
		return nil, err
	}
	cardNode := nodes[plan.Args.CharacterNodeID]
	meta, _ := cardNode["metadata"].(map[string]any)
	meta["characterVersionId"] = bound.Character.VersionID
	meta["characterVoiceStatus"] = bound.Character.VoiceStatus
	if bound.Character.Voice != nil {
		meta["characterVoiceName"] = bound.Character.Voice.Profile.Name
	}
	cardNode["metadata"] = meta
	if err := saveCloudAgentDocument(repo, canvas, doc, policy); err != nil {
		return nil, err
	}
	appliedPreview := cloudAgentCanvasAppliedPreview(plan.Preview)
	if len(recorder) > 0 && recorder[0] != nil {
		if err := recorder[0](repo, cloudAgentMutationInput{UserID: userID, CanvasID: canvasID, StepID: call.ID, Operation: call.Function.Name, BeforeSnapshotHash: beforeHash, AfterSnapshotHash: cloudAgentCanvasHash(doc), BeforeJSON: beforeJSON, Preview: &appliedPreview}); err != nil {
			return nil, err
		}
	}
	return map[string]any{"canvasId": canvasID, "nodeId": plan.Args.CharacterNodeID, "characterAssetId": plan.AssetID, "versionId": bound.Character.VersionID, "voiceStatus": bound.Character.VoiceStatus, "snapshotHash": cloudAgentCanvasHash(doc), "summary": appliedPreview.Description, "preview": appliedPreview}, nil
}
