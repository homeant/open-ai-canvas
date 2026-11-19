package app

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

type DramaAudioScope struct {
	DramaID      string `json:"dramaId"`
	EpisodeID    string `json:"episodeId"`
	SegmentIndex int    `json:"segmentIndex"`
}

// All times are relative to the segment. SourceOffsetMs selects the audio asset's in point.
type DramaAudioCueInput struct {
	ID             string           `json:"id"`
	ShotID         string           `json:"shotId"`
	Kind           string           `json:"kind" jsonschema:"speech 或 sfx"`
	DialogueMode   string           `json:"dialogueMode" jsonschema:"dialogue/inner_monologue/voice_over/silent"`
	Speaker        string           `json:"speaker"`
	Text           string           `json:"text"`
	StartMs        int64            `json:"startMs"`
	EndMs          int64            `json:"endMs"`
	SourceOffsetMs int64            `json:"sourceOffsetMs"`
	Model          DramaModelChoice `json:"model"`
	Voice          string           `json:"voice"`
	Instructions   string           `json:"instructions"`
}

type DramaAudioCue struct {
	DramaAudioCueInput
	TaskID     string `json:"taskId,omitempty"`
	ResourceID string `json:"resourceId,omitempty"`
	Source     string `json:"source"`
}

type DramaAudioVersionView struct {
	model.DramaAudioVersion
	Tracks []DramaAudioCue `json:"tracks"`
}

type DramaAudioShotWindow struct {
	ShotID       string `json:"shotId"`
	Position     int    `json:"position"`
	StartMs      int64  `json:"startMs"`
	EndMs        int64  `json:"endMs"`
	DialogueMode string `json:"dialogueMode"`
	Speaker      string `json:"speaker"`
	Text         string `json:"text"`
	MouthRule    string `json:"mouthRule"`
}

type dramaAudioTarget struct {
	segment model.DramaSegment
	shots   []model.DramaShot
	windows []DramaAudioShotWindow
	totalMs int64
}

func (s *Service) dramaAudioTarget(userID string, scope DramaAudioScope) (dramaAudioTarget, error) {
	var target dramaAudioTarget
	if scope.SegmentIndex < 0 {
		return target, BadAuthRequest("剧情段序号无效")
	}
	if _, _, err := s.dramaForUser(userID, scope.DramaID); err != nil {
		return target, err
	}
	if _, err := s.repo.DramaEpisode(scope.DramaID, scope.EpisodeID); err != nil {
		return target, err
	}
	segments, err := s.repo.DramaSegments(scope.EpisodeID)
	if err != nil {
		return target, err
	}
	for _, segment := range segments {
		if segment.SegmentIndex == scope.SegmentIndex {
			target.segment = segment
			break
		}
	}
	if target.segment.ID == "" {
		return target, NotFound("剧情段不存在")
	}
	shots, err := s.repo.DramaShots(scope.EpisodeID)
	if err != nil {
		return target, err
	}
	target.windows = []DramaAudioShotWindow{}
	for _, shot := range shots {
		if shot.SegmentIndex != scope.SegmentIndex {
			continue
		}
		mode := shot.DialogueMode
		if mode == "" {
			mode = "silent"
			if strings.TrimSpace(shot.Dialogue) != "" {
				mode = "dialogue"
			}
		}
		rule := "closed"
		if mode == "dialogue" {
			rule = "speaking"
		}
		target.shots = append(target.shots, shot)
		target.windows = append(target.windows, DramaAudioShotWindow{ShotID: shot.ID, Position: shot.Position, StartMs: target.totalMs, EndMs: target.totalMs + shot.DurationMs, DialogueMode: mode, Speaker: shot.Speaker, Text: shot.Dialogue, MouthRule: rule})
		target.totalMs += shot.DurationMs
	}
	return target, nil
}

func (s *Service) dramaAudioVersion(projectID, segmentID string, revision int64) (*DramaAudioVersionView, []model.DramaAudioVersion, error) {
	rows, err := s.repo.DramaAudioVersions(projectID, segmentID, revision)
	if err != nil || len(rows) == 0 {
		return nil, rows, err
	}
	view := &DramaAudioVersionView{DramaAudioVersion: rows[0], Tracks: []DramaAudioCue{}}
	if err := json.Unmarshal([]byte(rows[0].TracksJSON), &view.Tracks); err != nil {
		return nil, nil, err
	}
	return view, rows, nil
}

// DramaSegmentAudio never infers actual speech from a task prompt or an AAC stream.
func (s *Service) DramaSegmentAudio(userID string, scope DramaAudioScope, revision int64) (map[string]any, error) {
	if revision < 0 {
		return nil, BadAuthRequest("音轨版本无效")
	}
	target, err := s.dramaAudioTarget(userID, scope)
	if err != nil {
		return nil, err
	}
	version, history, err := s.dramaAudioVersion(scope.DramaID, target.segment.ID, revision)
	if err != nil {
		return nil, err
	}
	if revision > 0 && version == nil {
		return nil, NotFound("音轨版本不存在")
	}
	tasks := []map[string]any{}
	stale := false
	if version != nil {
		stale = version.ShotRevision != dramaShotRevision(target.shots, scope.SegmentIndex)
		for _, cue := range version.Tracks {
			if cue.TaskID == "" {
				continue
			}
			task, err := s.repo.Task(cue.TaskID)
			if err != nil {
				return nil, err
			}
			if task.UserID != userID || task.ProjectID != scope.DramaID {
				return nil, Forbidden("配音任务不属于当前作品")
			}
			tasks = append(tasks, map[string]any{"cueId": cue.ID, "taskId": task.ID, "status": task.Status, "stage": task.Stage, "error": taskForOutput(*task).Error, "requestedText": cue.Text, "actualTranscript": nil, "resultResourceId": dramaTaskResource(task), "boundResourceId": cue.ResourceID})
		}
	}
	return map[string]any{"version": version, "versions": history, "shotRevision": dramaShotRevision(target.shots, scope.SegmentIndex), "shotWindows": target.windows, "durationMs": target.totalMs, "stale": stale, "tasks": tasks, "currentVideoResourceId": target.segment.VideoResourceID, "qaStatus": "unreviewed", "note": "音轨版本是后期计划，不会替换本段视频；requestedText 不是实际转写。独白/旁白的声源在画外，mouthRule 是待检查的要求。"}, nil
}

type SaveDramaAudioRequest struct {
	DramaAudioScope
	ExpectedAudioRevision int64                `json:"expectedAudioRevision"`
	ExpectedShotRevision  string               `json:"expectedShotRevision"`
	VideoTakeID           string               `json:"videoTakeId"`
	Tracks                []DramaAudioCueInput `json:"tracks"`
	IdempotencyKey        string               `json:"idempotencyKey"`
}

func checkDramaAudioRevision(target dramaAudioTarget, before *DramaAudioVersionView, revision int64, shotRevision string) error {
	current := int64(0)
	if before != nil {
		current = before.Revision
	}
	actual := dramaShotRevision(target.shots, target.segment.SegmentIndex)
	if revision < 0 || current != revision || shotRevision == "" || actual != shotRevision {
		return dramaMCPConflict(map[string]any{"audioRevision": current, "shotRevision": actual})
	}
	return nil
}

func validateDramaAudioCues(inputs []DramaAudioCueInput, target dramaAudioTarget) error {
	if len(inputs) > 100 {
		return BadAuthRequest("单段最多 100 个音频片段")
	}
	windows := map[string]DramaAudioShotWindow{}
	for _, window := range target.windows {
		windows[window.ShotID] = window
	}
	seen := map[string]bool{}
	for _, cue := range inputs {
		w, ok := windows[cue.ShotID]
		if cue.ID == "" || len(cue.ID) > 80 || seen[cue.ID] || !ok {
			return BadAuthRequest("音频片段须有唯一 ID 并绑定本段 shotId")
		}
		seen[cue.ID] = true
		if cue.StartMs < w.StartMs || cue.EndMs > w.EndMs || cue.EndMs <= cue.StartMs || cue.SourceOffsetMs < 0 || cue.SourceOffsetMs > 3600000 {
			return BadAuthRequest("音频片段时间窗必须落在绑定镜头内")
		}
		if utf8.RuneCountInString(cue.Text) > 5000 || utf8.RuneCountInString(cue.Instructions) > 1000 || len(cue.Voice) > 120 || utf8.RuneCountInString(cue.Speaker) > 60 {
			return BadAuthRequest("配音文本或声源信息过长")
		}
		if cue.Kind == "sfx" {
			if cue.DialogueMode != "silent" || strings.TrimSpace(cue.Text) == "" {
				return BadAuthRequest("音效须为 silent 模式，text 填音效说明")
			}
			continue
		}
		if cue.Kind != "speech" || cue.DialogueMode == "silent" || cue.DialogueMode != w.DialogueMode || strings.TrimSpace(cue.Speaker) == "" || cue.Speaker != w.Speaker || strings.TrimSpace(cue.Text) == "" {
			return BadAuthRequest("配音模式和说话人须与分镜一致；静默镜头不能生成对白")
		}
		if cue.DialogueMode != "dialogue" && cue.DialogueMode != "inner_monologue" && cue.DialogueMode != "voice_over" {
			return BadAuthRequest("配音模式无效")
		}
	}
	return nil
}

func (s *Service) saveDramaAudioVersion(userID string, scope DramaAudioScope, target dramaAudioTarget, before *DramaAudioVersionView, takeID, resourceID string, cues []DramaAudioCue) (any, error) {
	data, err := json.Marshal(cues)
	if err != nil {
		return nil, err
	}
	revision := int64(1)
	if before != nil {
		revision = before.Revision + 1
	}
	row := model.DramaAudioVersion{ID: newID(), ProjectID: scope.DramaID, EpisodeID: scope.EpisodeID, SegmentID: target.segment.ID, Revision: revision, ShotRevision: dramaShotRevision(target.shots, scope.SegmentIndex), VideoTakeID: takeID, VideoResourceID: resourceID, TracksJSON: string(data), CreatedBy: userID, CreatedAt: time.Now()}
	resources := []string{resourceID}
	for _, cue := range cues {
		resources = append(resources, cue.ResourceID)
	}
	if err := s.repo.CreateDramaAudioVersion(&row, resources); err != nil {
		return nil, err
	}
	return DramaAudioVersionView{DramaAudioVersion: row, Tracks: cues}, nil
}

func (s *Service) SaveDramaSegmentAudio(userID string, req SaveDramaAudioRequest) (map[string]any, error) {
	return s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "audio.save", req.IdempotencyKey, req, func(local *Service) (any, error) {
		target, err := local.dramaAudioTarget(userID, req.DramaAudioScope)
		if err != nil {
			return nil, err
		}
		before, _, err := local.dramaAudioVersion(req.DramaID, target.segment.ID, 0)
		if err != nil {
			return nil, err
		}
		if err := checkDramaAudioRevision(target, before, req.ExpectedAudioRevision, req.ExpectedShotRevision); err != nil {
			return nil, err
		}
		if err := validateDramaAudioCues(req.Tracks, target); err != nil {
			return nil, err
		}
		videoResourceID := ""
		if req.VideoTakeID != "" {
			take, err := local.repo.DramaTake(req.DramaID, req.VideoTakeID)
			if err != nil || take == nil || take.Kind != "video" || take.OwnerID != target.segment.ID {
				return nil, BadAuthRequest("videoTakeId 必须属于本段")
			}
			if _, err := local.repo.ResourceForUser(userID, take.ResourceID); err != nil {
				return nil, err
			}
			videoResourceID = take.ResourceID
		}
		old := map[string]DramaAudioCue{}
		if before != nil {
			for _, cue := range before.Tracks {
				old[cue.ID] = cue
			}
		}
		cues := []DramaAudioCue{}
		for _, input := range req.Tracks {
			cue := DramaAudioCue{DramaAudioCueInput: input, Source: "unbound"}
			if prior, ok := old[input.ID]; ok && dramaContentRevision(prior.DramaAudioCueInput) == dramaContentRevision(input) && before.ShotRevision == req.ExpectedShotRevision {
				cue = prior
			}
			cues = append(cues, cue)
		}
		return local.saveDramaAudioVersion(userID, req.DramaAudioScope, target, before, req.VideoTakeID, videoResourceID, cues)
	})
}

type MutateDramaAudioCueRequest struct {
	DramaAudioScope
	ExpectedAudioRevision int64  `json:"expectedAudioRevision"`
	ExpectedShotRevision  string `json:"expectedShotRevision"`
	CueID                 string `json:"cueId"`
	ResourceID            string `json:"resourceId,omitempty"`
	MaxCostMicrocredits   int64  `json:"maxCostMicrocredits"`
	IdempotencyKey        string `json:"idempotencyKey"`
}

func (s *Service) prepareDramaAudioTask(userID string, scope DramaAudioScope, cue DramaAudioCue, limit int64, holdsStorage bool) (*model.Task, *model.BillingOrder, error) {
	if cue.Kind != "speech" || cue.DialogueMode == "silent" || cue.Model.Empty() || strings.TrimSpace(cue.Voice) == "" || limit < 0 {
		return nil, nil, BadAuthRequest("仅配音可提交 TTS，须明确模型、音色及非负费用上限；音效请绑定素材")
	}
	if cue.Model.LogicalModelID != "" && (cue.Model.ChannelID != "" || cue.Model.ModelKey != "") {
		return nil, nil, BadAuthRequest("配音模型须选择逻辑模型或系统渠道之一")
	}
	args := cloudAgentMediaArgs{Mode: "audio", Prompt: cue.Text, ChannelID: cue.Model.ChannelID, ChannelModelKey: cue.Model.ModelKey, Voice: cue.Voice, VoiceInstruction: cue.Instructions}
	if err := s.validateCloudAgentQwenAudio(&args); err != nil {
		return nil, nil, err
	}
	preparation := &creationTaskPreparation{}
	config := map[string]any{"audioVoice": cue.Voice, "audioInstructions": cue.Instructions, "audioFormat": "mp3"}
	if cue.Model.ChannelID != "" {
		config["channelId"], config["channelModelKey"] = cue.Model.ChannelID, cue.Model.ModelKey
		config["model"] = cue.Model.ModelKey
	}
	req := CreateTaskRequest{ProjectID: scope.DramaID, Type: "canvas_audio", Operation: "text_to_speech", Prompt: cue.Text, LogicalModelID: cue.Model.LogicalModelID, Input: map[string]any{"mode": "audio", "prompt": cue.Text, "config": config, "metadata": map[string]any{"source": "drama-audio", "episodeId": scope.EpisodeID, "segmentIndex": scope.SegmentIndex, "cueId": cue.ID}}, creationPrepare: preparation, callerHoldsStorageMu: holdsStorage, admission: &taskAdmission{ID: newID(), MaxCharge: limit}}
	task, err := s.CreateTask(userID, req)
	return task, preparation.Order, err
}

func (s *Service) MutateDramaAudioCue(userID, command string, req MutateDramaAudioCueRequest) (map[string]any, error) {
	if command != "generate" && command != "bind" {
		return nil, BadAuthRequest("音轨操作无效")
	}
	// Share the live service's storage lock; persist admission and the plan in one transaction.
	s.storageMu.Lock()
	result, err := s.runDramaMutation(userID, req.DramaID, req.EpisodeID, "audio."+command, req.IdempotencyKey, req, func(local *Service) (any, error) {
		local.pendingStorage = s.pendingStorage
		target, err := local.dramaAudioTarget(userID, req.DramaAudioScope)
		if err != nil {
			return nil, err
		}
		before, _, err := local.dramaAudioVersion(req.DramaID, target.segment.ID, 0)
		if err != nil {
			return nil, err
		}
		if err := checkDramaAudioRevision(target, before, req.ExpectedAudioRevision, req.ExpectedShotRevision); err != nil {
			return nil, err
		}
		if before == nil || before.ShotRevision != req.ExpectedShotRevision {
			return nil, BadAuthRequest("请先保存与现行分镜一致的音轨计划")
		}
		found := false
		for i := range before.Tracks {
			cue := &before.Tracks[i]
			if cue.ID != req.CueID {
				continue
			}
			found = true
			if command == "generate" {
				if err := local.repo.LockDramaAudioBudget(req.DramaID); err != nil {
					return nil, err
				}
				if cue.TaskID != "" || cue.ResourceID != "" || req.ResourceID != "" {
					return nil, BadAuthRequest("已有任务或资源，请先保存新的音频片段 ID 再重生成")
				}
				task, order, err := local.prepareDramaAudioTask(userID, req.DramaAudioScope, *cue, req.MaxCostMicrocredits, true)
				if err != nil {
					return nil, err
				}
				policy, err := local.RuntimePolicy()
				if err != nil {
					return nil, err
				}
				_, production, err := local.dramaForUser(userID, req.DramaID)
				if err != nil {
					return nil, err
				}
				totals, err := local.dramaBillingTotals(production)
				if err != nil {
					return nil, err
				}
				charge := int64(0)
				if order != nil {
					charge = order.AmountMicrocredits
					task.BillingOrderID = order.ID
				}
				if production.BudgetMicrocredits > 0 && charge > production.BudgetMicrocredits-totals.ChargedMicrocredits()-totals.HeldMicrocredits() {
					return nil, BadAuthRequest("配音报价超过作品剩余预算")
				}
				var input map[string]any
				if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
					return nil, err
				}
				if err := local.protectTaskSecrets(input); err != nil {
					return nil, err
				}
				data, err := json.Marshal(input)
				if err != nil {
					return nil, err
				}
				task.InputJSON = string(data)
				if err := createTaskWithStorageQuotaRepository(local.repo, task, order, policy); err != nil {
					return nil, err
				}
				cue.TaskID, cue.Source = task.ID, "tts"
			} else {
				id := req.ResourceID
				if cue.TaskID != "" {
					task, err := local.repo.Task(cue.TaskID)
					if err != nil || task == nil || task.UserID != userID || task.ProjectID != req.DramaID || task.Status != model.TaskStatusSucceeded {
						return nil, BadAuthRequest("配音任务尚未成功")
					}
					id = dramaTaskResource(task)
					if id == "" || (req.ResourceID != "" && req.ResourceID != id) {
						return nil, BadAuthRequest("资源不是该配音任务的实际产物")
					}
				}
				resource, err := local.repo.ResourceForUser(userID, id)
				if err != nil || resource == nil || resource.Status != model.ResourceStatusReady || resource.Kind != "audio" || !strings.HasPrefix(resource.MimeType, "audio/") {
					return nil, BadAuthRequest("请绑定当前用户已就绪的音频资源")
				}
				if resource.DurationMs <= 0 || cue.SourceOffsetMs+cue.EndMs-cue.StartMs > resource.DurationMs {
					return nil, BadAuthRequest("音频实测时长未知或不足以覆盖片段；请调整时间窗")
				}
				cue.ResourceID = id
				if cue.TaskID == "" {
					cue.Source = "import"
				}
			}
		}
		if !found {
			return nil, NotFound("音频片段不存在")
		}
		return local.saveDramaAudioVersion(userID, req.DramaAudioScope, target, before, before.VideoTakeID, before.VideoResourceID, before.Tracks)
	})
	s.storageMu.Unlock()
	if err == nil && command == "generate" {
		s.wakeTaskDispatcher()
	}
	return result, err
}

func (s *Service) PreviewDramaSegmentAudio(userID string, scope DramaAudioScope) (map[string]any, error) {
	target, err := s.dramaAudioTarget(userID, scope)
	if err != nil {
		return nil, err
	}
	version, _, err := s.dramaAudioVersion(scope.DramaID, target.segment.ID, 0)
	if err != nil {
		return nil, err
	}
	blockers := []string{}
	quotes := []map[string]any{}
	var total int64
	if version == nil {
		blockers = append(blockers, "尚未保存音轨计划")
	} else {
		if version.ShotRevision != dramaShotRevision(target.shots, scope.SegmentIndex) {
			blockers = append(blockers, "分镜已变化，须重存计划并重新验收")
		}
		for _, cue := range version.Tracks {
			if cue.ResourceID != "" {
				resource, err := s.repo.ResourceForUser(userID, cue.ResourceID)
				if err != nil || resource == nil || resource.Status != model.ResourceStatusReady || resource.DurationMs < cue.SourceOffsetMs+cue.EndMs-cue.StartMs {
					blockers = append(blockers, cue.ID+": 已绑定音频不可用或时长不足")
				}
				continue
			}
			if cue.TaskID != "" {
				blockers = append(blockers, cue.ID+": 等待任务完成后绑定实际产物")
				continue
			}
			if cue.Kind == "sfx" {
				blockers = append(blockers, cue.ID+": 音效尚未绑定资源")
				continue
			}
			_, order, err := s.prepareDramaAudioTask(userID, scope, cue, math.MaxInt64, false)
			if err != nil {
				blockers = append(blockers, cue.ID+": "+err.Error())
				continue
			}
			charge := int64(0)
			if order != nil {
				charge = order.AmountMicrocredits
			}
			if charge > math.MaxInt64-total {
				return nil, BadAuthRequest("配音报价超出范围")
			}
			total += charge
			quotes = append(quotes, map[string]any{"cueId": cue.ID, "text": cue.Text, "voice": cue.Voice, "amountMicrocredits": charge})
		}
	}
	account, err := s.repo.CreditAccount(userID)
	if err != nil {
		return nil, err
	}
	_, production, err := s.dramaForUser(userID, scope.DramaID)
	if err != nil {
		return nil, err
	}
	totals, err := s.dramaBillingTotals(production)
	if err != nil {
		return nil, err
	}
	remaining := int64(-1)
	if production.BudgetMicrocredits > 0 {
		remaining = production.BudgetMicrocredits - totals.ChargedMicrocredits() - totals.HeldMicrocredits()
		if total > remaining {
			blockers = append(blockers, "配音总报价超过作品剩余预算")
		}
	}
	if total > account.AvailableMicrocredits {
		blockers = append(blockers, "账号可用积分不足")
	}
	return map[string]any{"version": version, "shotWindows": target.windows, "quotes": quotes, "totalMicrocredits": total, "availableMicrocredits": account.AvailableMicrocredits, "remainingProjectBudgetMicrocredits": remaining, "blockers": blockers, "canGenerate": len(blockers) == 0, "audioAccepted": false, "note": fmt.Sprintf("报价仅包含尚未提交的配音；须逐片段显式提交并绑定实际产物。计划时长 %d ms；文本、说话人、嘴部及音效仍需验收。", target.totalMs)}, nil
}
