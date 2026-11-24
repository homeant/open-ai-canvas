package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

func TestDramaShotInformationBindingDryRunAndActualRequest(t *testing.T) {
	s, fake, project, episode, before := mcpEditingFixture(t)
	image := fake.resource("usr-drama", "image", "image/png")
	refs := []model.DramaShotImageReference{{ResourceID: image, Role: "information", Name: "手机屏内消息"}}
	req := DramaShotImagesRequest{DramaID: project, EpisodeID: episode, ShotID: before.Shots[0].ID, ExpectedEpisodeRevision: before.EpisodeRevision, References: refs, DryRun: true}
	preview, err := s.BindDramaShotImages("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	request := preview["request"].(DramaSegmentRequest)
	if preview["referencePreparationOK"] != true || len(request.ShotImages) != 1 || !request.ShotImages[0].Sent || request.ShotImages[0].ResourceID != image || request.ShotImages[0].InputOrder != 0 {
		t.Fatalf("critical input %+v", preview)
	}
	after, err := s.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("dry run changed formal state")
	}
	var receipts int64
	fake.db.Model(&model.DramaMutationReceipt{}).Where("episode_id = ?", episode).Count(&receipts)
	if receipts != 0 {
		t.Fatal("dry run wrote a receipt")
	}
	req.DryRun, req.IdempotencyKey = false, "bind-info"
	result, err := s.BindDramaShotImages("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.BindDramaShotImages("usr-drama", req)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatal("binding not idempotent")
	}
	after, err = s.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil || after.Segment.PromptStatus != model.DramaPromptConflict || len(after.Segment.Request.ShotImages) != 1 || !after.Segment.Request.ShotImages[0].Sent {
		t.Fatalf("binding %+v %v", after, err)
	}
	resource, _ := s.repo.ResourceForUser("usr-drama", image)
	if err := s.repo.DeleteDetachedResources([]model.Resource{*resource}, nil); err != repository.ErrResourceCleanupStillReferenced {
		t.Fatalf("critical image deleted %v", err)
	}
	video := dramaVideoFixture()
	video.References.MaxImages = 0
	setDramaVideoCapability(t, s, fake, video)
	blocked, err := s.PreviewDramaSegmentGeneration("usr-drama", project, episode, 0)
	if err != nil || blocked["canSubmit"] != false || !strings.Contains(blocked["request"].(DramaSegmentRequest).PreparationError, "关键镜头") {
		t.Fatalf("missing critical image not blocked %+v %v", blocked, err)
	}
}

func TestDramaShotCanvasImageSourceAndFirstFrame(t *testing.T) {
	s, fake, project, episode, before := mcpEditingFixture(t)
	image := fake.resource("usr-drama", "image", "image/png")
	doc := map[string]any{"nodes": []any{map[string]any{"id": "phone", "type": "image", "title": "准确手机图", "metadata": map[string]any{"status": "success", "storageKey": "resource:" + image}}}, "connections": []any{}}
	data, _ := json.Marshal(doc)
	if err := fake.db.Create(&model.CanvasProject{ID: "image-canvas", ProjectID: project, UserID: "usr-drama", PayloadJSON: string(data)}).Error; err != nil {
		t.Fatal(err)
	}
	candidates, err := s.DramaCanvasImageCandidates("usr-drama", DramaCanvasImageCandidatesRequest{DramaID: project, EpisodeID: episode, CanvasID: "image-canvas"})
	if err != nil {
		t.Fatal(err)
	}
	item := candidates["images"].([]map[string]any)[0]
	req := DramaShotImagesRequest{DramaID: project, EpisodeID: episode, ShotID: before.Shots[0].ID, ExpectedEpisodeRevision: before.EpisodeRevision, DryRun: true, References: []model.DramaShotImageReference{{Role: "first_frame", Name: "手机首帧", CanvasID: "image-canvas", NodeID: "phone", NodeHash: item["nodeHash"].(string)}}}
	result, err := s.BindDramaShotImages("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	request := result["request"].(DramaSegmentRequest)
	if request.Operation != "image_to_video" || request.FirstFrameResourceID != image || request.FirstFrameShotID != before.Shots[0].ID || !request.ShotImages[0].Sent {
		t.Fatalf("wrong first frame %+v", request)
	}
	req.References[0].NodeHash = "stale"
	if _, err := s.BindDramaShotImages("usr-drama", req); err == nil {
		t.Fatal("stale node accepted")
	}
	if _, err := s.DramaCanvasImageCandidates("other", DramaCanvasImageCandidatesRequest{DramaID: project, EpisodeID: episode, CanvasID: "image-canvas"}); err == nil {
		t.Fatal("foreign canvas access")
	}
}

func TestUpdateDramaShotRemovesAndRefreshesCanvasImageBinding(t *testing.T) {
	s, fake, project, _, before := mcpEditingFixture(t)
	oldImage := fake.resource("usr-drama", "image", "image/png")
	newImage := fake.resource("usr-drama", "image", "image/png")
	doc := map[string]any{"nodes": []any{map[string]any{"id": "phone", "type": "image", "title": "手机画面", "metadata": map[string]any{"status": "success", "storageKey": "resource:" + oldImage}}}, "connections": []any{}}
	data, _ := json.Marshal(doc)
	canvas := model.CanvasProject{ID: "refresh-canvas", ProjectID: project, UserID: "usr-drama", PayloadJSON: string(data)}
	if err := fake.db.Create(&canvas).Error; err != nil {
		t.Fatal(err)
	}
	refs := []model.DramaShotImageReference{{ResourceID: oldImage, Role: "information", Name: "手机画面", CanvasID: canvas.ID, NodeID: "phone", NodeHash: cloudAgentNodeHash(doc, "phone")}}
	encoded, _ := json.Marshal(refs)
	shotID := before.Shots[0].ID
	if err := fake.db.Model(&model.DramaShot{}).Where("id = ?", shotID).Update("image_references_json", string(encoded)).Error; err != nil {
		t.Fatal(err)
	}
	doc["nodes"].([]any)[0].(map[string]any)["metadata"].(map[string]any)["storageKey"] = "resource:" + newImage
	data, _ = json.Marshal(doc)
	if err := fake.db.Model(&model.CanvasProject{}).Where("id = ?", canvas.ID).Update("payload_json", string(data)).Error; err != nil {
		t.Fatal(err)
	}
	refreshed, err := s.UpdateDramaShot("usr-drama", project, shotID, UpdateDramaShotRequest{ImageReferenceAction: &DramaShotImageReferenceAction{Operation: "refresh_canvas", ResourceID: oldImage}})
	if err != nil {
		t.Fatal(err)
	}
	updated := dramaShotImages(refreshed)
	if len(updated) != 1 || updated[0].ResourceID != newImage || updated[0].NodeHash != cloudAgentNodeHash(doc, "phone") {
		t.Fatalf("canvas binding not refreshed: %+v", updated)
	}
	removed, err := s.UpdateDramaShot("usr-drama", project, shotID, UpdateDramaShotRequest{ImageReferenceAction: &DramaShotImageReferenceAction{Operation: "remove", ResourceID: newImage}})
	if err != nil {
		t.Fatal(err)
	}
	if refs := dramaShotImages(removed); len(refs) != 0 {
		t.Fatalf("shot binding not removed: %+v", refs)
	}
}

func TestDramaShotMediaRejectionAndStaticBoundary(t *testing.T) {
	s, fake, project, episode, before := mcpEditingFixture(t)
	scope := DramaShotMediaScope{DramaID: project, EpisodeID: episode, ShotID: before.Shots[0].ID}
	read, err := s.DramaShotMediaVersions("usr-drama", scope)
	if err != nil {
		t.Fatal(err)
	}
	revision := read["shotRevision"].(string)
	image := fake.resource("usr-drama", "image", "image/png")
	registered, err := s.RegisterDramaShotMedia("usr-drama", RegisterDramaShotMediaRequest{DramaShotMediaScope: scope, ExpectedShotRevision: revision, ResourceID: image, DurationMs: before.Shots[0].DurationMs, IdempotencyKey: "static-candidate"})
	if err != nil {
		t.Fatal(err)
	}
	takeID := registered["take"].(map[string]any)["id"].(string)
	review := DramaShotMediaReviewRequest{DramaShotMediaScope: scope, TakeID: takeID, ExpectedShotRevision: revision, Layer: "text", Status: "rejected", Reason: "四行牌重复首行，拒绝", EndMs: before.Shots[0].DurationMs, IdempotencyKey: "reject-text"}
	if _, err := s.ReviewDramaShotMedia("usr-drama", review); err != nil {
		t.Fatal(err)
	}
	read, err = s.DramaShotMediaVersions("usr-drama", scope)
	if err != nil || read["versions"].([]map[string]any)[0]["qa"].(map[string]any)["status"] != "rejected" {
		t.Fatalf("lost rejection %+v %v", read, err)
	}
	review.Layer, review.IdempotencyKey = "continuity", "reject-static-motion"
	if _, err := s.ReviewDramaShotMedia("usr-drama", review); err == nil {
		t.Fatal("static image approved for motion")
	}
	// Existing generated video can be rejected even when its old script fingerprint is unknown.
	review.TakeID, review.Layer, review.IdempotencyKey = before.VideoVersions[0].ID, "text", "reject-formal-take"
	if _, err := s.ReviewDramaShotMedia("usr-drama", review); err != nil {
		t.Fatal(err)
	}
	versions, err := s.DramaSegmentVideoVersions("usr-drama", project, episode, 0)
	if err != nil || versions.Versions[0].QAStatus != "rejected" {
		t.Fatalf("ready hides rejection %+v %v", versions, err)
	}
	after, err := s.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("candidate or review replaced formal media")
	}
}
