package app

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"yingce/backend/internal/model"
	"yingce/backend/internal/production"
	"yingce/backend/internal/repository"
)

type DramaTimingRequest struct {
	DramaID   string `json:"dramaId"`
	EpisodeID string `json:"episodeId"`
}

type DramaTimingEdit struct {
	Operation  string `json:"operation" jsonschema:"retime 或 merge_next；合并相邻镜支持跨段，保留首镜 shotId"`
	ShotID     string `json:"shotId"`
	NextShotID string `json:"nextShotId,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

type PreviewDramaTimingRequest struct {
	DramaTimingRequest
	ExpectedEpisodeRevision string            `json:"expectedEpisodeRevision"`
	ExpectedTimingRevision  string            `json:"expectedTimingRevision"`
	DryRun                  bool              `json:"dryRun"`
	Edits                   []DramaTimingEdit `json:"edits"`
}

type DramaTimingWindow struct {
	model.DramaShot
	EpisodeStartMs       int64    `json:"episodeStartMs"`
	EpisodeEndMs         int64    `json:"episodeEndMs"`
	SegmentStartMs       int64    `json:"segmentStartMs"`
	SegmentEndMs         int64    `json:"segmentEndMs"`
	DialogueStartMs      int64    `json:"dialogueStartMs"`
	DialogueEndMs        int64    `json:"dialogueEndMs"`
	RequiredSpeechMs     int64    `json:"requiredSpeechMs"`
	DialogueTiming       string   `json:"dialogueTimingSource"`
	ResolvedDialogueMode string   `json:"resolvedDialogueMode"`
	MouthRule            string   `json:"mouthRule"`
	SourceShotIDs        []string `json:"sourceShotIds"`
}

func timingDialogueMode(shot model.DramaShot) string {
	if shot.DialogueMode != "" {
		return shot.DialogueMode
	}
	if strings.TrimSpace(shot.Dialogue) != "" {
		return "dialogue"
	}
	return "silent"
}

func dramaTimingWindows(shots []model.DramaShot, settings DramaSettings, sources map[string][]string) []DramaTimingWindow {
	result := []DramaTimingWindow{}
	var elapsed int64
	offsets := map[int]int64{}
	for _, shot := range shots {
		mode := timingDialogueMode(shot)
		mouth := "closed"
		if mode == "dialogue" {
			mouth = "speaking"
		} else if mode == "voice_over" {
			mouth = "offscreen_or_closed"
		}
		speech := int64(0)
		if mode != "silent" {
			speech = settings.Storyboard.SpokenMs(shot.Dialogue)
		}
		ids := sources[shot.ID]
		if len(ids) == 0 {
			ids = []string{shot.ID}
		}
		result = append(result, DramaTimingWindow{DramaShot: shot, EpisodeStartMs: elapsed, EpisodeEndMs: elapsed + shot.DurationMs,
			SegmentStartMs: offsets[shot.SegmentIndex], SegmentEndMs: offsets[shot.SegmentIndex] + shot.DurationMs,
			DialogueStartMs: elapsed, DialogueEndMs: elapsed + speech, RequiredSpeechMs: speech,
			DialogueTiming: "estimated_from_script_not_measured", ResolvedDialogueMode: mode, MouthRule: mouth, SourceShotIDs: ids})
		elapsed += shot.DurationMs
		offsets[shot.SegmentIndex] += shot.DurationMs
	}
	return result
}

func dramaTimingRevisions(snapshot repository.DramaTimingSnapshot) (string, string) {
	return dramaShotRevision(snapshot.Shots, -1), dramaContentRevision([]any{snapshot.Shots, snapshot.Segments, snapshot.Audio, snapshot.AudioChecks, snapshot.Production.SettingsJSON})
}

func (s *Service) readDramaTiming(userID string, req DramaTimingRequest) (repository.DramaTimingSnapshot, error) {
	if _, _, err := s.dramaForUser(userID, req.DramaID); err != nil {
		return repository.DramaTimingSnapshot{}, err
	}
	return s.repo.DramaTimingSnapshot(req.DramaID, req.EpisodeID)
}

func latestTimingAudio(snapshot repository.DramaTimingSnapshot) map[string]DramaAudioVersionView {
	result := map[string]DramaAudioVersionView{}
	for _, version := range snapshot.Audio {
		if _, exists := result[version.SegmentID]; exists {
			continue
		}
		var tracks []DramaAudioCue
		// Parsing errors are handled by the caller before returning any plan.
		_ = json.Unmarshal([]byte(version.TracksJSON), &tracks)
		result[version.SegmentID] = DramaAudioVersionView{DramaAudioVersion: version, Tracks: tracks}
	}
	return result
}

func validateTimingAudio(snapshot repository.DramaTimingSnapshot) error {
	for _, version := range snapshot.Audio {
		var tracks []DramaAudioCue
		if err := json.Unmarshal([]byte(version.TracksJSON), &tracks); err != nil {
			return fmt.Errorf("读取音轨版本 %s: %w", version.ID, err)
		}
	}
	return nil
}

func timingAudioInspection(snapshot repository.DramaTimingSnapshot) map[string]any {
	result := map[string]any{}
	for segmentID, version := range latestTimingAudio(snapshot) {
		rows := []model.DramaAudioCheck{}
		for _, check := range snapshot.AudioChecks {
			if check.VersionID == version.ID {
				rows = append(rows, check)
			}
		}
		states := map[string]any{}
		for key, check := range latestDramaAudioChecks(rows) {
			var details any
			parseErr := json.Unmarshal([]byte(check.ResultJSON), &details)
			states[key] = map[string]any{"checkId": check.ID, "layer": check.Layer, "status": check.Status, "method": check.Method,
				"resourceId": check.ResourceID, "result": details, "resultReadable": parseErr == nil}
		}
		result[segmentID] = map[string]any{"audioVersionId": version.ID, "audioRevision": version.Revision,
			"latestChecks": states, "missingCheckStatus": "unverified", "acceptance": "not_inferred_from_audio_presence_or_tts_success"}
	}
	return result
}

func (s *Service) DramaEpisodeTiming(userID string, req DramaTimingRequest) (map[string]any, error) {
	snapshot, err := s.readDramaTiming(userID, req)
	if err != nil {
		return nil, err
	}
	if err := validateTimingAudio(snapshot); err != nil {
		return nil, err
	}
	episodeRevision, timingRevision := dramaTimingRevisions(snapshot)
	settings := parseDramaSettings(snapshot.Production.SettingsJSON)
	windows := dramaTimingWindows(snapshot.Shots, settings, nil)
	short := []string{}
	for _, window := range windows {
		if window.DurationMs < 2000 {
			short = append(short, window.ID)
		}
	}
	return map[string]any{"episodeId": req.EpisodeID, "episodeRevision": episodeRevision, "episodeRecordRevision": snapshot.Episode.Revision,
		"timingRevision": timingRevision, "shots": windows, "segments": snapshot.Segments, "audioVersions": latestTimingAudio(snapshot),
		"shorterThan2SecondsShotIds": short, "timeUnit": "ms", "dialogueTimingMeasured": false,
		"audioTimingSource": "saved_segment_relative_plan_not_asr", "audioInspection": timingAudioInspection(snapshot),
		"asrServiceConfigured": strings.TrimSpace(os.Getenv("CANVAS_WHISPER_BASE_URL")) != "", "asrAvailability": "not_probed", "readOnly": true}, nil
}

func mergeTimingStrings(first, second string) string {
	if strings.TrimSpace(first) == "" {
		return second
	}
	if strings.TrimSpace(second) == "" {
		return first
	}
	return first + "\n" + second
}

func mergeTimingIDs(first, second string) string {
	ids := decodeStringList(first)
	for _, id := range decodeStringList(second) {
		if !containsString(ids, id) {
			ids = append(ids, id)
		}
	}
	encoded, _ := json.Marshal(ids)
	return string(encoded)
}

func applyTimingEdits(shots []model.DramaShot, edits []DramaTimingEdit) ([]model.DramaShot, map[string][]string, map[int]bool, error) {
	shots = append([]model.DramaShot(nil), shots...)
	sources, affected := map[string][]string{}, map[int]bool{}
	for _, shot := range shots {
		sources[shot.ID] = []string{shot.ID}
	}
	for _, edit := range edits {
		pos := -1
		for i := range shots {
			if shots[i].ID == edit.ShotID && edit.ShotID != "" {
				pos = i
				break
			}
		}
		if pos < 0 {
			return nil, nil, nil, BadAuthRequest("shotId 不属于当前预演镜头列表")
		}
		first := &shots[pos]
		switch edit.Operation {
		case "retime":
			if edit.NextShotID != "" || edit.DurationMs < 1000 || edit.DurationMs > production.MaxStoryboardShotSeconds*1000 {
				return nil, nil, nil, BadAuthRequest("retime 需明确 1000–60000ms，不接受 nextShotId")
			}
			if first.DurationMs != edit.DurationMs {
				affected[first.SegmentIndex] = true
				first.DurationMs = edit.DurationMs
			}
		case "merge_next":
			if pos+1 >= len(shots) || edit.NextShotID == "" || shots[pos+1].ID != edit.NextShotID {
				return nil, nil, nil, BadAuthRequest("merge_next 必须明确相邻的 nextShotId")
			}
			next := shots[pos+1]
			if first.Scene != next.Scene || first.SceneCastID != next.SceneCastID {
				return nil, nil, nil, BadAuthRequest("不同场景不能合并为同一表演镜")
			}
			if strings.TrimSpace(first.Dialogue) != "" && strings.TrimSpace(next.Dialogue) != "" && (first.Speaker != next.Speaker || timingDialogueMode(*first) != timingDialogueMode(next)) {
				return nil, nil, nil, BadAuthRequest("不同说话人或对白模式不能合并为单一表演镜")
			}
			duration := first.DurationMs + next.DurationMs
			if edit.DurationMs != 0 {
				duration = edit.DurationMs
			}
			if duration < 1000 || duration > production.MaxStoryboardShotSeconds*1000 {
				return nil, nil, nil, BadAuthRequest("合并镜头时长须为 1000–60000ms")
			}
			if strings.TrimSpace(first.Dialogue) == "" {
				first.Speaker, first.DialogueMode = next.Speaker, next.DialogueMode
			}
			first.Dialogue = mergeTimingStrings(first.Dialogue, next.Dialogue)
			first.Action = mergeTimingStrings(first.Action, next.Action)
			first.ImagePrompt = mergeTimingStrings(first.ImagePrompt, next.ImagePrompt)
			first.VideoPrompt = mergeTimingStrings(first.VideoPrompt, next.VideoPrompt)
			first.SourceQuote = mergeTimingStrings(first.SourceQuote, next.SourceQuote)
			if utf8.RuneCountInString(first.Dialogue) > dramaMaxDialogueRunes || utf8.RuneCountInString(first.Action) > 1000 || utf8.RuneCountInString(first.ImagePrompt) > 2000 || utf8.RuneCountInString(first.VideoPrompt) > 2000 {
				return nil, nil, nil, BadAuthRequest("合并正文超出字段限制，请先人工整理表演提示词")
			}
			first.CastIDsJSON = mergeTimingIDs(first.CastIDsJSON, next.CastIDsJSON)
			first.PropIDsJSON = mergeTimingIDs(first.PropIDsJSON, next.PropIDsJSON)
			first.LookCastIDsJSON = mergeTimingIDs(first.LookCastIDsJSON, next.LookCastIDsJSON)
			refs := append(dramaShotImages(*first), dramaShotImages(next)...)
			if len(refs) > 0 {
				encoded, _ := json.Marshal(refs)
				first.ImageReferencesJSON = string(encoded)
			}
			first.DurationMs = duration
			affected[first.SegmentIndex], affected[next.SegmentIndex] = true, true
			sources[first.ID] = append(sources[first.ID], sources[next.ID]...)
			delete(sources, next.ID)
			shots = append(shots[:pos+1], shots[pos+2:]...)
		default:
			return nil, nil, nil, BadAuthRequest("operation 需为 retime / merge_next")
		}
	}
	for i := range shots {
		shots[i].Position = i
	}
	return shots, sources, affected, nil
}

func sortedTimingIndexes(values map[int]bool) []int {
	result := []int{}
	for index := range values {
		result = append(result, index)
	}
	sort.Ints(result)
	return result
}
