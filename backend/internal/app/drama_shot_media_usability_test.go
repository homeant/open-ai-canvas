package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"yingce/backend/internal/model"
)

func TestDramaCanvasImageCandidatesPaginationAndResourceLookup(t *testing.T) {
	s, fake, project, episode, _ := mcpEditingFixture(t)
	common := fake.resource("usr-drama", "image", "image/png")
	nodes := []map[string]any{}
	lateResources := []string{}
	for i := 0; i < 231; i++ {
		resource := common
		if i >= 227 {
			resource = fake.resource("usr-drama", "image", "image/png")
			lateResources = append(lateResources, resource)
		}
		nodes = append(nodes, map[string]any{"id": fmt.Sprintf("img-%03d", i), "type": "image", "title": "候选", "metadata": map[string]any{"status": "success", "storageKey": "resource:" + resource}})
	}
	foreign := fake.resource("other", "image", "image/png")
	nodes = append(nodes, map[string]any{"id": "foreign", "type": "image", "metadata": map[string]any{"storageKey": "resource:" + foreign}}, map[string]any{"id": "missing", "type": "image", "metadata": map[string]any{"storageKey": "resource:missing"}}, map[string]any{"id": "text", "type": "text"})
	doc := map[string]any{"nodes": nodes, "connections": []any{}}
	data, _ := json.Marshal(doc)
	if err := fake.db.Create(&model.CanvasProject{ID: "candidate-canvas", UserID: "usr-drama", ProjectID: project, PayloadJSON: string(data)}).Error; err != nil {
		t.Fatal(err)
	}
	req := DramaCanvasImageCandidatesRequest{DramaID: project, EpisodeID: episode, CanvasID: "candidate-canvas"}
	seen := map[string]bool{}
	for page := 0; page < 3; page++ {
		result, err := s.DramaCanvasImageCandidates("usr-drama", req)
		if err != nil {
			t.Fatal(err)
		}
		images := result["images"].([]map[string]any)
		want := 100
		if page == 2 {
			want = 31
		}
		if len(images) != want || result["total"] != 231 || result["hasMore"] != (page < 2) {
			t.Fatalf("page %d: %+v", page, result)
		}
		for _, image := range images {
			id := image["nodeId"].(string)
			if seen[id] || image["nodeHash"] == "" {
				t.Fatal("duplicate or incomplete image", image)
			}
			seen[id] = true
		}
		req.Offset = result["nextOffset"].(int)
		req.SnapshotHash = result["snapshotHash"].(string)
	}
	if len(seen) != 231 || !seen["img-230"] {
		t.Fatal("late uploads were lost")
	}
	req.Offset, req.ResourceID = 0, lateResources[3]
	filtered, err := s.DramaCanvasImageCandidates("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	if filtered["total"] != 1 || filtered["images"].([]map[string]any)[0]["nodeId"] != "img-230" {
		t.Fatalf("resource filter %+v", filtered)
	}
	req.ResourceID = foreign
	filtered, err = s.DramaCanvasImageCandidates("usr-drama", req)
	if err != nil || filtered["total"] != 0 {
		t.Fatal("foreign image leaked", err)
	}
	req.ResourceID = ""
	req.Offset = math.MaxInt
	filtered, err = s.DramaCanvasImageCandidates("usr-drama", req)
	if err != nil || len(filtered["images"].([]map[string]any)) != 0 || filtered["hasMore"] != false {
		t.Fatal("overflow page", err)
	}
	for _, bad := range []DramaCanvasImageCandidatesRequest{{DramaID: project, EpisodeID: episode, CanvasID: "candidate-canvas", Offset: -1}, {DramaID: project, EpisodeID: episode, CanvasID: "candidate-canvas", Limit: 101}} {
		if _, err := s.DramaCanvasImageCandidates("usr-drama", bad); err == nil {
			t.Fatal("invalid pagination accepted")
		}
	}
	nodes[0]["title"] = "编辑后"
	data, _ = json.Marshal(doc)
	if err := fake.db.Model(&model.CanvasProject{}).Where("id = ?", "candidate-canvas").Update("payload_json", string(data)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.DramaCanvasImageCandidates("usr-drama", req); err == nil {
		t.Fatal("stale pagination snapshot accepted")
	}
	if _, err := s.DramaCanvasImageCandidates("other", req); err == nil {
		t.Fatal("cross-account lookup accepted")
	}
}

func TestDramaShotMediaReviewLayerAndRevisionScopes(t *testing.T) {
	s, fake, project, episode, before := mcpEditingFixture(t)
	scope := DramaShotMediaScope{DramaID: project, EpisodeID: episode, ShotID: before.Shots[0].ID}
	current := dramaShotRevision([]model.DramaShot{before.Shots[0]}, -1)
	image := fake.resource("usr-drama", "image", "image/png")
	registered, err := s.RegisterDramaShotMedia("usr-drama", RegisterDramaShotMediaRequest{DramaShotMediaScope: scope, ExpectedShotRevision: current, ResourceID: image, DurationMs: before.Shots[0].DurationMs, IdempotencyKey: "scope-image"})
	if err != nil {
		t.Fatal(err)
	}
	takeID := registered["take"].(map[string]any)["id"].(string)
	req := DramaShotMediaReviewRequest{DramaShotMediaScope: scope, TakeID: takeID, ExpectedShotRevision: current, Layer: "picture", Status: "approved", Reason: "实体屏幕附着正确", EndMs: before.Shots[0].DurationMs, IdempotencyKey: "picture"}
	picture, err := s.ReviewDramaShotMedia("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	pictureID := picture["id"].(string)
	req.Layer, req.IdempotencyKey, req.ExpectedReviewID = "text", "text-wrong-layer", pictureID
	_, err = s.ReviewDramaShotMedia("usr-drama", req)
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Status != 409 || appErr.Reason != "shot_media_review_revision_conflict" {
		t.Fatalf("scope conflict %v", err)
	}
	details := appErr.Details
	if details["layer"] != "text" || details["currentReviewId"] != "" || details["expectedReviewIdScope"] != "takeId+shotId+layer" {
		t.Fatalf("ambiguous conflict %+v", details)
	}
	req.ExpectedReviewID = ""
	req.IdempotencyKey = "text-first"
	req.Status = "rejected"
	req.Reason = "动态文字未合格"
	text, err := s.ReviewDramaShotMedia("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.DramaShotMediaVersions("usr-drama", scope)
	if err != nil {
		t.Fatal(err)
	}
	qa := read["versions"].([]map[string]any)[0]["qa"].(map[string]any)
	ids := qa["latestReviewIds"].(map[string]string)
	if ids["picture"] != pictureID || ids["text"] != text["id"] || ids["continuity"] != "" || qa["status"] != "rejected" || qa["acceptedForVideo"] != false {
		t.Fatalf("layer identities or QA lost %+v", qa)
	}
	// A video's generation fingerprint covers its segment; approval still CASes
	// the current individual shot. Do not require these hashes to be equal.
	extra := before.Shots[0]
	extra.ID = "scope-extra-shot"
	extra.Position = 100
	extra.DurationMs = 1000
	if err := fake.db.Create(&extra).Error; err != nil {
		t.Fatal(err)
	}
	shots, err := s.repo.DramaShots(episode)
	if err != nil {
		t.Fatal(err)
	}
	segmentRevision := dramaShotRevision(shots, before.Shots[0].SegmentIndex)
	if segmentRevision == current {
		t.Fatal("fixture must have multiple segment shots")
	}
	videoTake := before.VideoVersions[0].ID
	if err := fake.db.Model(&model.DramaTake{}).Where("id = ?", videoTake).Update("shot_revision", segmentRevision).Error; err != nil {
		t.Fatal(err)
	}
	read, err = s.DramaShotMediaVersions("usr-drama", scope)
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, version := range read["versions"].([]map[string]any) {
		if version["take"].(model.DramaTake).ID == videoTake {
			matched = version["scriptMatch"] == "matched" && version["takeShotRevisionScope"] == "segment"
		}
	}
	if !matched || read["reviewExpectedShotRevision"] != current || read["shotRevisionScope"] != "single_shot" {
		t.Fatal("revision semantics lost")
	}
	req.TakeID = videoTake
	req.ExpectedReviewID = ""
	req.ExpectedShotRevision = segmentRevision
	req.IdempotencyKey = "wrong-segment-hash"
	_, err = s.ReviewDramaShotMedia("usr-drama", req)
	if !errors.As(err, &appErr) || appErr.Reason != "shot_media_shot_revision_conflict" {
		t.Fatalf("revision conflict %v", err)
	}
	details = appErr.Details
	if details["expectedShotRevision"] != current || details["expectedShotRevisionScope"] != "single_shot" || details["takeShotRevisionScope"] != "segment" {
		t.Fatalf("current revision missing %+v", details)
	}
	req.ExpectedShotRevision = current
	req.IdempotencyKey = "correct-shot-hash"
	if _, err := s.ReviewDramaShotMedia("usr-drama", req); err != nil {
		t.Fatal(err)
	}
}

func TestDramaShotImageReferenceNumberMappingReceipts(t *testing.T) {
	s, fake, project, episode, before := mcpEditingFixture(t)
	if len(before.Segment.Request.References) == 0 {
		t.Fatal("fixture needs existing numbered references")
	}
	prompt := "参考图1只用于原角色，不要更换人物。"
	if err := fake.db.Model(&model.DramaSegment{}).Where("id = ?", before.Segment.ID).Update("video_prompt_override", prompt).Error; err != nil {
		t.Fatal(err)
	}
	before, err := s.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	image := fake.resource("usr-drama", "image", "image/png")
	req := DramaShotImagesRequest{DramaID: project, EpisodeID: episode, ShotID: before.Shots[0].ID, ExpectedEpisodeRevision: before.EpisodeRevision, References: []model.DramaShotImageReference{{ResourceID: image, Role: "information", Name: "新手机屏幕"}}, DryRun: true}
	preview, err := s.BindDramaShotImages("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	reports := preview["referenceChanges"].([]dramaReferenceNumberingReport)
	if len(reports) != 1 || !reports[0].NumberingChanged || !reports[0].PromptReviewRequired || !reports[0].ComparisonComplete || len(reports[0].PromptConflicts) != 1 {
		t.Fatalf("missing renumber report %+v", reports)
	}
	conflict := reports[0].PromptConflicts[0]
	if conflict.Token != "参考图1" || conflict.SuggestedLabel != "参考图2" || conflict.CurrentResourceID != image || conflict.PreviousResourceID != before.Segment.Request.References[0].ResourceID {
		t.Fatalf("wrong identity mapping %+v", conflict)
	}
	after, err := s.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("preview rewrote formal state")
	}
	req.DryRun = false
	req.IdempotencyKey = "save-numbering"
	saved, err := s.BindDramaShotImages("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(saved["referenceChanges"])
	var savedReports []dramaReferenceNumberingReport
	if err := json.Unmarshal(data, &savedReports); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reports, savedReports) {
		t.Fatalf("preview/save mapping drift %+v %+v", reports, savedReports)
	}
	replay, err := s.BindDramaShotImages("usr-drama", req)
	if err != nil || !reflect.DeepEqual(saved, replay) {
		t.Fatal("mapping receipt replay changed", err)
	}
	segment, _ := s.repo.DramaSegment(project, before.Segment.ID)
	if segment.VideoPromptOverride != prompt || segment.PromptStatus != model.DramaPromptConflict {
		t.Fatal("prompt rewritten or silently confirmed")
	}
}

func TestDramaReferenceNumberMappingDistinguishesFramesAndDuplicateResources(t *testing.T) {
	old := DramaSegmentReference{CastID: "actor", ResourceID: "same-file", Label: "参考图1", Sent: true, InputOrder: 0}
	shifted := old
	shifted.InputOrder = 1
	before := dramaShotImagePlan{Request: DramaSegmentRequest{References: []DramaSegmentReference{old}}}
	after := dramaShotImagePlan{Request: DramaSegmentRequest{References: []DramaSegmentReference{shifted}, FirstFrameResourceID: "frame"}}
	report := dramaReferenceNumberingChanges(0, before, after, "使用参考图1")
	if report.NumberingChanged || len(report.PromptConflicts) != 0 || !report.PromptReviewRequired {
		t.Fatal("first frame incorrectly renumbered reference")
	}
	info := DramaSegmentReference{ShotID: "phone", Role: "information", ResourceID: "same-file", Label: "参考图1", Sent: true, InputOrder: 0}
	shifted.Label = "参考图2"
	after.Request.References = []DramaSegmentReference{info, shifted}
	report = dramaReferenceNumberingChanges(0, before, after, "参考图1和参考图10")
	if report.Mapping[0].After.CastID != "actor" || report.Mapping[0].After.Label != "参考图2" || report.PromptConflicts[0].SuggestedLabel != "参考图2" || len(report.PromptConflicts) != 2 {
		t.Fatalf("merged different uses of same file %+v", report)
	}
}
