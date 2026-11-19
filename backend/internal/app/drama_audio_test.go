package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"yingce/backend/internal/model"
)

func dramaAudioFixture(t *testing.T) (*Service, *fakeDramaBackend, SaveDramaAudioRequest) {
	t.Helper()
	s, fake, project, episode, detail := mcpEditingFixture(t)
	mode, speaker, dialogue := "inner_monologue", "陈启", "这次，我不能再五五分。"
	if _, err := s.EditDramaShots("usr-drama", project, episode, EditDramaShotsRequest{ExpectedEpisodeRevision: detail.EpisodeRevision, IdempotencyKey: "prepare-inner", Edits: []DramaShotEdit{{Operation: "update", ShotID: detail.Shots[0].ID, DialogueMode: &mode, UpdateDramaShotRequest: UpdateDramaShotRequest{Speaker: &speaker, Dialogue: &dialogue}}}}); err != nil {
		t.Fatal(err)
	}
	detail, err := s.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&model.ModelChannel{ID: "audio-ch", Scope: model.ChannelScopeSystem, Enabled: true, Name: "隔离配音"},
		&model.ChannelModel{ID: "audio-model", ChannelID: "audio-ch", ModelKey: "tts", Capability: "audio", Protocol: model.ChannelInterfaceOpenAIAudio, CapabilityConfigJSON: mustEncodeModelCapabilityConfig(t, DefaultModelCapabilityConfigForModel(string(model.ChannelInterfaceOpenAIAudio), "tts")), BillingMode: "fixed_request", UnitPriceMicrocredits: 100, PriceConfigured: true, Enabled: true},
		&model.ChannelModelPriceTier{ID: "audio-tier", ChannelModelID: "audio-model", SelectorKey: "{}", SelectorJSON: "{}", BillingMode: "fixed_request", UnitPriceMicrocredits: 100, PriceConfigured: true, Enabled: true},
		&model.CreditAccount{UserID: "usr-drama", AvailableMicrocredits: 10000},
	} {
		if err := fake.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	cue := DramaAudioCueInput{ID: "inner-23", ShotID: detail.Shots[0].ID, Kind: "speech", DialogueMode: mode, Speaker: speaker, Text: dialogue, StartMs: 0, EndMs: detail.Shots[0].DurationMs, Model: DramaModelChoice{ChannelID: "audio-ch", ModelKey: "tts"}, Voice: "alloy"}
	return s, fake, SaveDramaAudioRequest{DramaAudioScope: DramaAudioScope{DramaID: project, EpisodeID: episode, SegmentIndex: 0}, ExpectedShotRevision: detail.Segment.ShotRevision, VideoTakeID: detail.VideoVersions[0].ID, Tracks: []DramaAudioCueInput{cue}, IdempotencyKey: "audio-plan"}
}

func TestDramaAudioPlanWindowsOwnershipAndVersions(t *testing.T) {
	s, fake, req := dramaAudioFixture(t)
	before, err := s.DramaSegmentDetail("usr-drama", req.DramaID, req.EpisodeID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*SaveDramaAudioRequest){
		"out-of-window": func(r *SaveDramaAudioRequest) { r.Tracks[0].EndMs++ },
		"wrong-mode":    func(r *SaveDramaAudioRequest) { r.Tracks[0].DialogueMode = "dialogue" },
		"foreign-shot":  func(r *SaveDramaAudioRequest) { r.Tracks[0].ShotID = "foreign" },
	} {
		bad := req
		bad.Tracks = append([]DramaAudioCueInput{}, req.Tracks...)
		bad.IdempotencyKey = name
		change(&bad)
		if _, err := s.SaveDramaSegmentAudio("usr-drama", bad); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	if _, err := s.SaveDramaSegmentAudio("other", req); err == nil {
		t.Fatal("foreign save")
	}
	result, err := s.SaveDramaSegmentAudio("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.SaveDramaSegmentAudio("usr-drama", req)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("plan replay %v", err)
	}
	if _, err := s.DramaSegmentAudio("other", req.DramaAudioScope, 0); err == nil {
		t.Fatal("foreign read")
	}
	read, err := s.DramaSegmentAudio("usr-drama", req.DramaAudioScope, 0)
	if err != nil || read["shotWindows"].([]DramaAudioShotWindow)[0].MouthRule != "closed" || read["qaStatus"] != "unreviewed" {
		t.Fatalf("read %+v %v", read, err)
	}
	preview, err := s.PreviewDramaSegmentAudio("usr-drama", req.DramaAudioScope)
	if err != nil || preview["canGenerate"] != true || preview["totalMicrocredits"] != int64(100) {
		t.Fatalf("preview %+v %v", preview, err)
	}
	var tasks int64
	fake.db.Model(&model.Task{}).Where("type = ?", "canvas_audio").Count(&tasks)
	if tasks != 0 {
		t.Fatal("preflight created a task")
	}
	after, err := s.DramaSegmentDetail("usr-drama", req.DramaID, req.EpisodeID, 0)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("audio plan modified formal video/script")
	}
	req.IdempotencyKey = "stale"
	if _, err := s.SaveDramaSegmentAudio("usr-drama", req); err == nil {
		t.Fatal("stale audio plan accepted")
	}
}

func TestDramaAudioAdmissionReplayAndOutputBinding(t *testing.T) {
	s, fake, plan := dramaAudioFixture(t)
	if _, err := s.SaveDramaSegmentAudio("usr-drama", plan); err != nil {
		t.Fatal(err)
	}
	req := MutateDramaAudioCueRequest{DramaAudioScope: plan.DramaAudioScope, ExpectedAudioRevision: 1, ExpectedShotRevision: plan.ExpectedShotRevision, CueID: "inner-23", MaxCostMicrocredits: 99, IdempotencyKey: "generate"}
	if _, err := s.MutateDramaAudioCue("usr-drama", "generate", req); err == nil {
		t.Fatal("price ceiling bypassed")
	}
	req.MaxCostMicrocredits = 100
	if err := fake.db.Model(&model.DramaProduction{}).Where("project_id = ?", plan.DramaID).Update("budget_microcredits", 50).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.MutateDramaAudioCue("usr-drama", "generate", req); err == nil {
		t.Fatal("project budget bypassed")
	}
	if err := fake.db.Model(&model.DramaProduction{}).Where("project_id = ?", plan.DramaID).Update("budget_microcredits", 10000).Error; err != nil {
		t.Fatal(err)
	}
	result, err := s.MutateDramaAudioCue("usr-drama", "generate", req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.MutateDramaAudioCue("usr-drama", "generate", req)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("generation replay %v", err)
	}
	var tasks []model.Task
	if err := fake.db.Where("type = ?", "canvas_audio").Find(&tasks).Error; err != nil || len(tasks) != 1 {
		t.Fatalf("duplicate tasks %d %v", len(tasks), err)
	}
	task := tasks[0]
	var input map[string]any
	if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil || task.Prompt != plan.Tracks[0].Text || input["config"].(map[string]any)["audioVoice"] != "alloy" {
		t.Fatal("lost text/voice")
	}
	account, err := s.repo.CreditAccount("usr-drama")
	if err != nil || account.ReservedMicrocredits != 100 {
		t.Fatalf("reservation %+v %v", account, err)
	}
	req.ExpectedAudioRevision, req.IdempotencyKey = 2, "bind"
	if _, err := s.MutateDramaAudioCue("usr-drama", "bind", req); err == nil {
		t.Fatal("pending output bound")
	}
	resource := fake.resource("usr-drama", "audio", "audio/mpeg")
	if err := fake.db.Model(&model.Resource{}).Where("id = ?", resource).Update("duration_ms", 6000).Error; err != nil {
		t.Fatal(err)
	}
	if err := fake.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{"status": model.TaskStatusSucceeded, "result_json": `{"mode":"audio","audio":{"storageKey":"resource:` + resource + `"}}`}).Error; err != nil {
		t.Fatal(err)
	}
	req.ResourceID = "foreign"
	if _, err := s.MutateDramaAudioCue("usr-drama", "bind", req); err == nil {
		t.Fatal("substituted task output")
	}
	req.ResourceID = ""
	if _, err := s.MutateDramaAudioCue("usr-drama", "bind", req); err != nil {
		t.Fatal(err)
	}
	read, err := s.DramaSegmentAudio("usr-drama", plan.DramaAudioScope, 0)
	if err != nil {
		t.Fatal(err)
	}
	version := read["version"].(*DramaAudioVersionView)
	if version.Revision != 3 || version.Tracks[0].ResourceID != resource || read["qaStatus"] != "unreviewed" {
		t.Fatalf("binding %+v", read)
	}
	refs, err := s.repo.DramaResourceReferences("usr-drama", []string{resource})
	if err != nil || len(refs) == 0 {
		t.Fatalf("audio reference unprotected %v", err)
	}
}
