package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

func TestTimingMergesAdjacentCrossSegmentPerformanceWithoutLosingRepeatedDialogue(t *testing.T) {
	shots := []model.DramaShot{
		{ID: "a", Position: 0, SegmentIndex: 5, Scene: "宿舍", SceneCastID: "room", Speaker: "陈启", DialogueMode: "inner_monologue", Dialogue: "不。", DurationMs: 1200, PropIDsJSON: `["book"]`},
		{ID: "b", Position: 1, SegmentIndex: 6, Scene: "宿舍", SceneCastID: "room", Speaker: "陈启", DialogueMode: "inner_monologue", Dialogue: "不。", DurationMs: 1300, PropIDsJSON: `["phone"]`},
		{ID: "c", Position: 2, SegmentIndex: 6, Scene: "宿舍", DurationMs: 3000},
	}
	copyBefore := append([]model.DramaShot(nil), shots...)
	after, sources, affected, err := applyTimingEdits(shots, []DramaTimingEdit{{Operation: "merge_next", ShotID: "a", NextShotID: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || after[0].ID != "a" || after[0].SegmentIndex != 5 || after[0].DurationMs != 2500 || after[0].Dialogue != "不。\n不。" || !affected[5] || !affected[6] || !reflect.DeepEqual(sources["a"], []string{"a", "b"}) || !reflect.DeepEqual(decodeStringList(after[0].PropIDsJSON), []string{"book", "phone"}) || !reflect.DeepEqual(shots, copyBefore) {
		t.Fatalf("cross-segment merge lost content or changed inputs: %+v %+v", after, sources)
	}
	for _, edit := range []DramaTimingEdit{
		{Operation: "merge_next", ShotID: "a", NextShotID: "c"},
		{Operation: "retime", ShotID: "missing", DurationMs: 3000},
		{Operation: "retime", ShotID: "a", DurationMs: 999},
		{Operation: "retime", ShotID: "a", DurationMs: 60001},
	} {
		if _, _, _, err := applyTimingEdits(shots, []DramaTimingEdit{edit}); err == nil {
			t.Fatalf("invalid edit accepted: %+v", edit)
		}
	}
	shots[1].Speaker = "周牧"
	if _, _, _, err := applyTimingEdits(shots, []DramaTimingEdit{{Operation: "merge_next", ShotID: "a", NextShotID: "b"}}); err == nil {
		t.Fatal("merged different speakers into one performance")
	}
}

func TestTimingCrossSegmentWarningKeepsSilentActionAndImplicitDialogueMode(t *testing.T) {
	shots := []model.DramaShot{
		{ID: "door", Position: 0, SegmentIndex: 9, Scene: "水果店", DurationMs: 1000, DialogueMode: "silent", Action: "把卷帘拉至半高"},
		{ID: "warning", Position: 1, SegmentIndex: 9, Scene: "水果店", DurationMs: 1800, Speaker: "老冯", DialogueMode: "dialogue", Dialogue: "八点前卖不完，"},
		{ID: "end", Position: 2, SegmentIndex: 10, Scene: "水果店", DurationMs: 2000, Speaker: "老冯", Dialogue: "我不陪你熬。"},
	}
	after, sources, affected, err := applyTimingEdits(shots, []DramaTimingEdit{{Operation: "merge_next", ShotID: "door", NextShotID: "warning"}, {Operation: "merge_next", ShotID: "door", NextShotID: "end"}})
	if err != nil || len(after) != 1 || after[0].DurationMs != 4800 || after[0].Speaker != "老冯" || after[0].Action != "把卷帘拉至半高" || after[0].Dialogue != "八点前卖不完，\n我不陪你熬。" || len(sources["door"]) != 3 || !affected[9] || !affected[10] {
		t.Fatalf("cross-segment warning lost original action/dialogue: %+v %v", after, err)
	}
}

func timingDatabaseFingerprint(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"drama_episodes", "drama_shots", "drama_segments", "drama_takes", "drama_mutation_receipts", "drama_jobs", "tasks", "credit_ledger_entries", "credit_accounts", "drama_audio_versions", "drama_audio_checks"} {
		var rows []map[string]any
		if err := db.Table(table).Order("id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		result[table] = dramaContentRevision(rows)
	}
	return result
}

func TestTimingPreviewIsReadOnlyQuotesAndReportsCrossSegmentAudioInvalidation(t *testing.T) {
	svc, fake, project, episode, detail := mcpEditingFixture(t)
	segments, err := svc.repo.DramaSegments(episode)
	if err != nil || len(segments) == 0 || len(detail.Shots) == 0 {
		t.Fatalf("missing fixture: %v", err)
	}
	first := segments[0]
	second := first
	second.ID, second.SegmentIndex = "timing-segment-1", 1
	for _, segment := range segments {
		if segment.SegmentIndex == 1 {
			second = segment
		}
	}
	if second.ID == "timing-segment-1" {
		if err := fake.db.Create(&second).Error; err != nil {
			t.Fatal(err)
		}
	}
	shots := make([]model.DramaShot, 3)
	for i := range shots {
		shots[i] = detail.Shots[0]
		shots[i].ID = []string{"timing-a", "timing-b", "timing-c"}[i]
		shots[i].Position, shots[i].SegmentIndex, shots[i].DurationMs = i, 1, 2000
		shots[i].Dialogue, shots[i].DialogueMode, shots[i].Speaker = "原句。", "inner_monologue", "陈启"
	}
	shots[0].SegmentIndex = 0
	if err := fake.db.Where("episode_id = ?", episode).Delete(&model.DramaShot{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fake.db.Create(&shots).Error; err != nil {
		t.Fatal(err)
	}
	tracks, _ := json.Marshal([]DramaAudioCue{{DramaAudioCueInput: DramaAudioCueInput{ID: "cue-b", ShotID: "timing-b", Kind: "speech", DialogueMode: "inner_monologue", Text: "原句。", StartMs: 0, EndMs: 1500}, Source: "uploaded"}})
	audio := model.DramaAudioVersion{ID: "timing-audio-1", ProjectID: project, EpisodeID: episode, SegmentID: second.ID, Revision: 1, ShotRevision: dramaShotRevision(shots, 1), TracksJSON: string(tracks), CreatedBy: "usr-drama", CreatedAt: time.Now()}
	if err := fake.db.Create(&audio).Error; err != nil {
		t.Fatal(err)
	}
	check := model.DramaAudioCheck{ID: "timing-check-unknown", ProjectID: project, EpisodeID: episode, SegmentID: second.ID, VersionID: audio.ID,
		Sequence: 1, CueID: "cue-b", Layer: "asr_cue", Status: "unknown", Method: "server_asr", ResultJSON: `{"measurementStatus":"unknown","reason":"ASR unavailable"}`, CreatedAt: time.Now()}
	if err := fake.db.Create(&check).Error; err != nil {
		t.Fatal(err)
	}
	scope := DramaTimingRequest{DramaID: project, EpisodeID: episode}
	beforeDB := timingDatabaseFingerprint(t, fake.db)
	timeline, err := svc.DramaEpisodeTiming("usr-drama", scope)
	if err != nil {
		t.Fatal(err)
	}
	inspection, _ := json.Marshal(timeline["audioInspection"])
	if !strings.Contains(string(inspection), `"status":"unknown"`) || !strings.Contains(string(inspection), `"method":"server_asr"`) || timeline["dialogueTimingMeasured"] != false {
		t.Fatalf("unknown ASR hidden or mistaken for measured dialogue: %s", inspection)
	}
	if _, err := svc.DramaEpisodeTiming("other", scope); err == nil {
		t.Fatal("timing leaked another user's project")
	}
	req := PreviewDramaTimingRequest{DramaTimingRequest: scope, ExpectedEpisodeRevision: timeline["episodeRevision"].(string), ExpectedTimingRevision: timeline["timingRevision"].(string), DryRun: true,
		Edits: []DramaTimingEdit{{Operation: "merge_next", ShotID: "timing-a", NextShotID: "timing-b"}}}
	result, err := svc.PreviewDramaTimingEdits("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	if result["applied"] != false || result["generationSubmitted"] != false || result["quoteIsAuthorization"] != false || !reflect.DeepEqual(result["directlyEditedSegmentIndexes"], []int{0, 1}) || len(result["after"].([]DramaTimingWindow)) != 2 || len(result["audioVersionsRequiringRecheck"].([]map[string]any)) != 1 {
		t.Fatalf("unsafe or incomplete preview: %+v", result)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), "audio_target_merged_or_moved") || !strings.Contains(string(encoded), "estimatedMicrocredits") || !strings.Contains(string(encoded), "原句。\\n原句。") {
		t.Fatalf("preview lacks audio, cost, or original dialogue: %s", encoded)
	}
	if result["estimateComplete"] == false && result["estimatedGenerationMicrocredits"] != nil {
		t.Fatal("unknown quote represented as a known total")
	}
	req.ExpectedTimingRevision = "stale"
	if _, err := svc.PreviewDramaTimingEdits("usr-drama", req); err == nil {
		t.Fatal("stale timing plan accepted")
	}
	req.DryRun = false
	if _, err := svc.PreviewDramaTimingEdits("usr-drama", req); err == nil {
		t.Fatal("write mode accepted")
	}
	if afterDB := timingDatabaseFingerprint(t, fake.db); !reflect.DeepEqual(beforeDB, afterDB) {
		t.Fatal("read and preview changed script, audio, jobs, tasks, receipts, or accounts")
	}
}
