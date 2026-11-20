package app

import (
	"errors"
	"reflect"
	"testing"

	"yingce/backend/internal/model"
)

// Reproduces EP01 segmentIndex 4: dormitory action with an office scene binding.
func mcpReferenceFixture(t *testing.T) (*Service, *fakeDramaBackend, string, string, DramaSegmentDetailResult, map[string]string) {
	t.Helper()
	svc, fake, project, episode, _ := mcpEditingFixture(t)
	ids := map[string]string{}
	for _, item := range []struct{ key, kind, name string }{{"chen", "character", "陈启"}, {"zhou", "character", "周牧"}, {"plan", "prop", "商业计划书"}, {"office", "scene", "前世鲜果快送办公室"}, {"dorm", "scene", "2013年培训学校宿舍"}} {
		id := "qa-" + item.key
		row := model.DramaCast{ID: id, ProjectID: project, Kind: item.kind, Name: item.name, ImageStatus: model.DramaMediaReady, ImageResourceID: fake.resource("usr-drama", "image", "image/png")}
		if err := fake.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		ids[item.key] = id
	}
	shot := model.DramaShot{ID: "qa-dorm-shot", ProjectID: project, EpisodeID: episode, Position: 100, SegmentIndex: 4, SegmentTitle: "陈启在宿舍稳住", Scene: "前世鲜果快送办公室", SceneCastID: ids["office"], CastIDsJSON: encodeStringList([]string{ids["chen"], ids["zhou"]}), PropIDsJSON: encodeStringList([]string{ids["plan"]}), LookCastIDsJSON: "[]", Action: "陈启在2013年宿舍放回证件，坐下听脚步", DurationMs: 2000, DialogueMode: "silent"}
	if err := fake.db.Create(&shot).Error; err != nil {
		t.Fatal(err)
	}
	segment := model.DramaSegment{ID: "qa-dorm-segment", ProjectID: project, EpisodeID: episode, SegmentIndex: 4, Revision: 61, PromptRevision: 4, PromptStatus: model.DramaMediaReady, VideoPrompt: "陈启坐在宿舍听脚步。", VideoStatus: model.DramaMediaReady}
	if err := fake.db.Create(&segment).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := svc.DramaSegmentDetail("usr-drama", project, episode, 4)
	if err != nil {
		t.Fatal(err)
	}
	return svc, fake, project, episode, detail, ids
}

func TestDramaMCPShotSceneAndAssetRebinding(t *testing.T) {
	svc, fake, project, episode, before, ids := mcpReferenceFixture(t)
	videos := len(dramaVideoRequests(fake))
	request := EditDramaShotsRequest{ExpectedEpisodeRevision: before.EpisodeRevision, IdempotencyKey: "rebind-dorm", Edits: []DramaShotEdit{{Operation: "update", ShotID: before.Shots[0].ID, SceneCastID: ptr(ids["dorm"]), CastIDs: ptr([]string{ids["chen"]}), PropIDs: ptr([]string{}), LookCastIDs: ptr([]string{})}}}
	result, err := svc.EditDramaShots("usr-drama", project, episode, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.EditDramaShots("usr-drama", project, episode, request)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("rebind replay: %v", err)
	}
	after, err := svc.DramaSegmentDetail("usr-drama", project, episode, 4)
	if err != nil {
		t.Fatal(err)
	}
	shot := after.Shots[0]
	if shot.SceneCastID != ids["dorm"] || shot.Scene != "2013年培训学校宿舍" || shot.PropIDsJSON != "[]" || !reflect.DeepEqual(decodeStringList(shot.CastIDsJSON), []string{ids["chen"]}) || after.Segment.PromptStatus != model.DramaPromptConflict {
		t.Fatalf("bindings not updated: %+v", after)
	}
	preview, err := svc.PreviewDramaSegmentGeneration("usr-drama", project, episode, 4)
	if err != nil {
		t.Fatal(err)
	}
	sent := preview["sentReferenceImages"].([]map[string]any)
	found := false
	for _, ref := range sent {
		if ref["castId"] == ids["dorm"] {
			found = true
		}
		if ref["castId"] == ids["office"] || ref["castId"] == ids["zhou"] || ref["castId"] == ids["plan"] {
			t.Fatalf("stale image sent: %#v", ref)
		}
	}
	if !found || preview["canSubmit"] != false || preview["episodeRevision"] != after.EpisodeRevision {
		t.Fatalf("preflight not refreshed: %#v", preview)
	}
	if len(dramaVideoRequests(fake)) != videos {
		t.Fatal("rebinding submitted generation")
	}
	request.IdempotencyKey = "stale-rebind"
	if _, err := svc.EditDramaShots("usr-drama", project, episode, request); err == nil {
		t.Fatal("stale episode version accepted")
	}
}

func TestDramaMCPReferencesRejectWrongProjectKindAndLookAtomically(t *testing.T) {
	svc, fake, project, episode, before, ids := mcpReferenceFixture(t)
	if err := fake.db.Create(&model.DramaCast{ID: "foreign-scene", ProjectID: "another-project", Kind: "scene"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fake.db.Create(&model.DramaCast{ID: "zhou-look", ProjectID: project, Kind: "character", ParentID: ids["zhou"]}).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []DramaShotEdit{
		{SceneCastID: ptr("foreign-scene")},
		{SceneCastID: ptr(ids["plan"])},
		{CastIDs: ptr([]string{ids["plan"]})},
		{PropIDs: ptr([]string{ids["chen"]})},
		{CastIDs: ptr([]string{ids["chen"]}), LookCastIDs: ptr([]string{"zhou-look"})},
	} {
		tc.Operation, tc.ShotID = "update", before.Shots[0].ID
		request := EditDramaShotsRequest{ExpectedEpisodeRevision: before.EpisodeRevision, IdempotencyKey: newID(), Edits: []DramaShotEdit{{Operation: "update", ShotID: before.Shots[0].ID, Scene: ptr("不应保存")}, tc}}
		if _, err := svc.EditDramaShots("usr-drama", project, episode, request); err == nil {
			t.Fatal("invalid binding accepted")
		}
		after, err := svc.DramaSegmentDetail("usr-drama", project, episode, 4)
		if err != nil || after.EpisodeRevision != before.EpisodeRevision {
			t.Fatalf("partial write: %v", err)
		}
	}
	request := EditDramaShotsRequest{ExpectedEpisodeRevision: before.EpisodeRevision, IdempotencyKey: "foreign-owner", Edits: []DramaShotEdit{{Operation: "update", ShotID: before.Shots[0].ID, SceneCastID: ptr(ids["dorm"])}}}
	if _, err := svc.EditDramaShots("another-user", project, episode, request); err == nil {
		t.Fatal("foreign user changed scene")
	}
}

func TestDramaMCPManualReferencesCleanupAndTextOnlyPreflight(t *testing.T) {
	svc, fake, project, episode, before, ids := mcpReferenceFixture(t)
	selected := []string{ids["office"], ids["zhou"], ids["plan"]}
	extra := []model.DramaReferenceImage{{ResourceID: fake.resource("usr-drama", "image", "image/png"), Name: "旧构图"}}
	if err := svc.UpdateDramaSegment("usr-drama", project, episode, 4, UpdateDramaSegmentRequest{ReferenceCastIDs: &selected, ReferenceImages: &extra}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EditDramaShots("usr-drama", project, episode, EditDramaShotsRequest{ExpectedEpisodeRevision: before.EpisodeRevision, IdempotencyKey: "fix-shot-under-custom", Edits: []DramaShotEdit{{Operation: "update", ShotID: before.Shots[0].ID, SceneCastID: ptr(ids["dorm"]), CastIDs: ptr([]string{ids["chen"]}), PropIDs: ptr([]string{})}}}); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewDramaSegmentGeneration("usr-drama", project, episode, 4)
	if err != nil {
		t.Fatal(err)
	}
	if preview["referenceMode"] != "custom" {
		t.Fatal("manual selection hidden")
	}
	detail, err := svc.DramaSegmentDetail("usr-drama", project, episode, 4)
	if err != nil {
		t.Fatal(err)
	}
	request := DramaMCPReferenceRequest{DramaID: project, EpisodeID: episode, SegmentIndex: 4, ExpectedSegmentRevision: detail.Segment.Revision, ExpectedShotRevision: detail.Segment.ShotRevision, IdempotencyKey: "restore-auto-and-clear-extras", AutoReferences: true, ReferenceImages: ptr([]model.DramaReferenceImage{})}
	result, err := svc.EditDramaSegmentReferencesMCP("usr-drama", request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.EditDramaSegmentReferencesMCP("usr-drama", request)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("reference replay: %v", err)
	}
	preview, err = svc.PreviewDramaSegmentGeneration("usr-drama", project, episode, 4)
	if err != nil {
		t.Fatal(err)
	}
	if preview["referenceMode"] != "auto" || preview["canSubmit"] != false {
		t.Fatal("auto selection/review not restored")
	}
	for _, ref := range preview["sentReferenceImages"].([]map[string]any) {
		if ref["castId"] == ids["office"] || ref["castId"] == ids["zhou"] || ref["castId"] == ids["plan"] || ref["resourceId"] == extra[0].ResourceID {
			t.Fatalf("unwanted image still sent: %#v", ref)
		}
	}
	request.IdempotencyKey = "stale-reference-edit"
	_, err = svc.EditDramaSegmentReferencesMCP("usr-drama", request)
	var conflict *AppError
	if !errors.As(err, &conflict) || conflict.Status != 409 || conflict.Details["shotRevision"] == nil {
		t.Fatalf("missing reference conflict details: %v", err)
	}
	// An existing asset can be text-only without being sent as an image.
	if err := svc.repo.UpdateDramaCastFields(ids["dorm"], map[string]any{"image_resource_id": "", "image_status": model.DramaMediaDraft}); err != nil {
		t.Fatal(err)
	}
	preview, err = svc.PreviewDramaSegmentGeneration("usr-drama", project, episode, 4)
	if err != nil {
		t.Fatal(err)
	}
	textOnly := preview["textOnlyReferences"].([]DramaSegmentReference)
	if len(textOnly) != 1 || textOnly[0].CastID != ids["dorm"] || textOnly[0].Sent {
		t.Fatalf("text-only asset indistinguishable from an image: %#v", textOnly)
	}
}
