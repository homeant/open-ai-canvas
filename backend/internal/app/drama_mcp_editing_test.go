package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"yingce/backend/internal/model"
)

func TestDramaMCPConcurrentEditsHaveOneWinner(t *testing.T) {
	svc, fake, project, episode, before := mcpEditingFixture(t)
	db, err := fake.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			<-start
			_, err := svc.EditDramaShots("usr-drama", project, episode, EditDramaShotsRequest{ExpectedEpisodeRevision: before.EpisodeRevision, IdempotencyKey: key, Edits: []DramaShotEdit{{Operation: "update", ShotID: before.Shots[0].ID, UpdateDramaShotRequest: UpdateDramaShotRequest{Dialogue: ptr(key)}}}})
			results <- err
		}(key)
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	conflict := 0
	for err := range results {
		if err == nil {
			success++
		} else if appErr, ok := err.(*AppError); ok && appErr.Status == 409 {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestDramaMCPComposeCompactReceipt(t *testing.T) {
	svc, _, project, episode, _ := mcpEditingFixture(t)
	row, err := svc.repo.DramaEpisode(project, episode)
	if err != nil {
		t.Fatal(err)
	}
	req := ComposeDramaMCPRequest{DramaID: project, EpisodeID: episode, ExpectedRevision: row.Revision, IdempotencyKey: "compose"}
	result, err := svc.ComposeDramaEpisodeMCP("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	if result["jobId"] == "" || result["shots"] != nil || len(result) > 9 {
		t.Fatalf("not compact %+v", result)
	}
	replay, err := svc.ComposeDramaEpisodeMCP("usr-drama", req)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("compose replay %+v %v", replay, err)
	}
	after, err := svc.repo.DramaEpisode(project, episode)
	if err != nil || row.Approved != after.Approved {
		t.Fatal("compose changed approval")
	}
}

func TestDramaMCPGenerationReplayDoesNotQueueTwice(t *testing.T) {
	svc, fake, project, episode, before := mcpEditingFixture(t)
	req := GenerateDramaMCPRequest{DramaID: project, EpisodeID: episode, Request: GenerateDramaSegmentsRequest{Segments: []int{0}, ExpectedPromptRevision: &before.Segment.Revision}, IdempotencyKey: "generate-once"}
	result, err := svc.GenerateDramaSegmentsMCP("usr-drama", req)
	if err != nil {
		t.Fatal(err)
	}
	runDramaTicks(t, svc, project, 6)
	count := len(dramaVideoRequests(fake))
	replay, err := svc.GenerateDramaSegmentsMCP("usr-drama", req)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("generation replay %+v %v", replay, err)
	}
	runDramaTicks(t, svc, project, 6)
	if len(dramaVideoRequests(fake)) != count {
		t.Fatal("replay submitted another video")
	}
	req.IdempotencyKey = "stale-generation"
	if _, err := svc.GenerateDramaSegmentsMCP("usr-drama", req); err == nil {
		t.Fatal("stale generation queued")
	}
}

func mcpEditingFixture(t *testing.T) (*Service, *fakeDramaBackend, string, string, DramaSegmentDetailResult) {
	t.Helper()
	svc, fake := newDramaTestService(t)
	project := createTestDrama(t, svc, DramaReviewAuto).Project.ID
	runDramaTicks(t, svc, project, 24)
	summary, err := svc.DramaSummary("usr-drama", project)
	if err != nil {
		t.Fatal(err)
	}
	episode := summary.Episodes[0].ID
	detail, err := svc.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	return svc, fake, project, episode, detail
}
func TestDramaMCPEpisodeSourcePreservesShotsAndReplay(t *testing.T) {
	svc, _, project, episode, before := mcpEditingFixture(t)
	source, err := svc.DramaEpisodeSource("usr-drama", project, episode)
	if err != nil {
		t.Fatal(err)
	}
	req := UpdateDramaEpisodeSourceRequest{SourceText: "陈启（内心独白）：这次，我不能再跟他五五分。", ChapterID: source.Chapters[0].ID, ExpectedSourceRevision: source.SourceRevision, IdempotencyKey: "source-1"}
	// Fixture episodes share chapter 2, but chapter 1 belongs only to episode 1.
	result, err := svc.UpdateDramaEpisodeSource("usr-drama", project, episode, req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.UpdateDramaEpisodeSource("usr-drama", project, episode, req)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("replay %v %v", replay, err)
	}
	after, err := svc.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Shots, after.Shots) || before.Segment.Revision != after.Segment.Revision || result["action"] != "saved_source_only" {
		t.Fatal("source-only save changed production")
	}
	req.IdempotencyKey = "source-stale"
	if _, err := svc.UpdateDramaEpisodeSource("usr-drama", project, episode, req); err == nil {
		t.Fatal("stale source accepted")
	}
	req.IdempotencyKey = "source-1"
	req.SourceText += "另一个请求"
	if _, err := svc.UpdateDramaEpisodeSource("usr-drama", project, episode, req); err == nil {
		t.Fatal("key reuse accepted")
	}
	if _, err := svc.DramaEpisodeSource("other", project, episode); err == nil {
		t.Fatal("foreign source read")
	}
	// A browser chapter save also invalidates the independent source revision.
	updated, err := svc.DramaEpisodeSource("usr-drama", project, episode)
	if err != nil {
		t.Fatal(err)
	}
	text := "网页刚保存的新原文"
	if _, err := svc.UpdateDramaChapter("usr-drama", project, req.ChapterID, UpdateDramaChapterRequest{Text: &text}); err != nil {
		t.Fatal(err)
	}
	req.IdempotencyKey = "browser-race"
	req.ExpectedSourceRevision = updated.SourceRevision
	if _, err := svc.UpdateDramaEpisodeSource("usr-drama", project, episode, req); err == nil {
		t.Fatal("browser source change not detected")
	}
}

func TestDramaMCPExplicitReadaptAndStaleCandidate(t *testing.T) {
	svc, _, project, episode, before := mcpEditingFixture(t)
	request := DramaMCPPromptRequest{DramaID: project, EpisodeID: episode, SegmentIndex: 0, ExpectedSegmentRevision: before.Segment.Revision, ExpectedShotRevision: before.Segment.ShotRevision, IdempotencyKey: "rewrite-before-edit"}
	if _, err := svc.MutateDramaSegmentPrompt("usr-drama", "rewrite", request); err != nil {
		t.Fatal(err)
	}
	runDramaTicks(t, svc, project, 6)
	candidate, err := svc.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EditDramaShots("usr-drama", project, episode, EditDramaShotsRequest{ExpectedEpisodeRevision: candidate.EpisodeRevision, IdempotencyKey: "change-candidate-input", Edits: []DramaShotEdit{{Operation: "update", ShotID: before.Shots[0].ID, UpdateDramaShotRequest: UpdateDramaShotRequest{Dialogue: ptr("候选生成后又修改")}}}}); err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "stale-candidate-adopt"
	request.ExpectedSegmentRevision = candidate.Segment.Revision
	request.CandidateID = candidate.Segment.CandidateID
	request.ExpectedSeconds = candidate.Segment.Request.Seconds
	request.Model = &candidate.Segment.VideoModel
	if _, err := svc.MutateDramaSegmentPrompt("usr-drama", "adopt", request); err == nil {
		t.Fatal("stale candidate adopted")
	}
	source, err := svc.DramaEpisodeSource("usr-drama", project, episode)
	if err != nil {
		t.Fatal(err)
	}
	req := UpdateDramaEpisodeSourceRequest{SourceText: "明确重新改编的测试原文。", ChapterID: source.Chapters[0].ID, ExpectedSourceRevision: source.SourceRevision, Readapt: true, IdempotencyKey: "explicit-readapt"}
	result, err := svc.UpdateDramaEpisodeSource("usr-drama", project, episode, req)
	if err != nil || result["action"] != "saved_and_readapt_queued" {
		t.Fatalf("readapt %+v %v", result, err)
	}
	shots, err := svc.repo.DramaShots(episode)
	if err != nil || len(shots) != 0 {
		t.Fatal("explicit readapt did not clear shots")
	}
	if _, err := svc.UpdateDramaEpisodeSource("usr-drama", project, episode, req); err != nil {
		t.Fatal("readapt retry was not replayed")
	}
}

func TestDramaMCPAtomicShotsDurationAndConflict(t *testing.T) {
	svc, fake, project, episode, before := mcpEditingFixture(t)
	anchor := before.Shots[0]
	req := EditDramaShotsRequest{ExpectedEpisodeRevision: before.EpisodeRevision, IdempotencyKey: "batch", Edits: []DramaShotEdit{
		{Operation: "insert_after", ShotID: anchor.ID, NewShotID: "monologue-1", DialogueMode: ptr("inner_monologue"), UpdateDramaShotRequest: UpdateDramaShotRequest{Speaker: ptr("陈启"), Dialogue: ptr("前世我们散了。"), DurationSeconds: ptr(2.0)}},
		{Operation: "insert_after", ShotID: "monologue-1", NewShotID: "monologue-2", DialogueMode: ptr("inner_monologue"), UpdateDramaShotRequest: UpdateDramaShotRequest{Dialogue: ptr("这次我不能五五分。"), DurationSeconds: ptr(2.0)}},
	}}
	result, err := svc.EditDramaShots("usr-drama", project, episode, req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.EditDramaShots("usr-drama", project, episode, req)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("replay %v", err)
	}
	after, err := svc.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Shots) != len(before.Shots)+2 || after.Shots[1].ID != "monologue-1" || after.Shots[2].ID != "monologue-2" || after.Segment.PromptStatus != model.DramaPromptConflict {
		t.Fatalf("bad atomic edit %+v", after)
	}
	if after.VideoVersions[0].ScriptMatch != "mismatch" {
		t.Fatalf("old video not flagged: %+v", after.VideoVersions)
	}
	req.IdempotencyKey = "stale"
	if _, err := svc.EditDramaShots("usr-drama", project, episode, req); err == nil {
		t.Fatal("stale batch accepted")
	}
	req.ExpectedEpisodeRevision = after.EpisodeRevision
	req.IdempotencyKey = "too-long"
	req.Edits = []DramaShotEdit{{Operation: "update", ShotID: anchor.ID, UpdateDramaShotRequest: UpdateDramaShotRequest{DurationSeconds: ptr(10.0)}}}
	if _, err := svc.EditDramaShots("usr-drama", project, episode, req); err == nil || !strings.Contains(err.Error(), "超出") {
		t.Fatalf("duration not rejected: %v", err)
	}
	unchanged, err := svc.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil || unchanged.EpisodeRevision != after.EpisodeRevision {
		t.Fatal("failed batch left partial edit")
	}
	req.IdempotencyKey = "partial-invalid"
	req.Edits = []DramaShotEdit{{Operation: "update", ShotID: anchor.ID, UpdateDramaShotRequest: UpdateDramaShotRequest{Dialogue: ptr("应回滚")}}, {Operation: "delete", ShotID: "missing"}}
	if _, err := svc.EditDramaShots("usr-drama", project, episode, req); err == nil {
		t.Fatal("invalid batch accepted")
	}
	row, err := svc.repo.DramaShot(project, anchor.ID)
	if err != nil || row.Dialogue != anchor.Dialogue {
		t.Fatal("partial edit committed")
	}
	var receipts int64
	if err := fake.db.Model(&model.DramaMutationReceipt{}).Count(&receipts).Error; err != nil || receipts != 1 {
		t.Fatalf("unexpected receipts %d %v", receipts, err)
	}
}

func TestDramaMCPAtomicEditsAcrossConsecutiveSegments(t *testing.T) {
	svc, _, project, episode, before := mcpEditingFixture(t)
	detail, err := svc.DramaEpisodeDetail("usr-drama", project, episode)
	if err != nil || len(detail.Segments) < 2 {
		t.Fatal("need consecutive segments")
	}
	var edits []DramaShotEdit
	for _, segment := range detail.Segments[:2] {
		edits = append(edits, DramaShotEdit{Operation: "update", ShotID: segment.ShotIDs[0], UpdateDramaShotRequest: UpdateDramaShotRequest{Dialogue: ptr("跨段独白" + itoa(segment.Index))}})
	}
	if _, err := svc.EditDramaShots("usr-drama", project, episode, EditDramaShotsRequest{ExpectedEpisodeRevision: before.EpisodeRevision, IdempotencyKey: "cross-segment", Edits: edits}); err != nil {
		t.Fatal(err)
	}
	for _, segment := range detail.Segments[:2] {
		read, err := svc.DramaSegmentDetail("usr-drama", project, episode, segment.Index)
		if err != nil || read.Shots[0].Dialogue != "跨段独白"+itoa(segment.Index) || read.Segment.PromptStatus != model.DramaPromptConflict {
			t.Fatalf("partial consecutive edit %+v %v", read, err)
		}
	}
}

func TestDramaMCPRewriteAdoptAndManualConfirmation(t *testing.T) {
	svc, fake, project, episode, before := mcpEditingFixture(t)
	db, err := fake.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	request := DramaMCPPromptRequest{DramaID: project, EpisodeID: episode, SegmentIndex: 0, ExpectedSegmentRevision: before.Segment.Revision, ExpectedShotRevision: before.Segment.ShotRevision, IdempotencyKey: "rewrite"}
	queued, err := svc.MutateDramaSegmentPrompt("usr-drama", "rewrite", request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.MutateDramaSegmentPrompt("usr-drama", "rewrite", request)
	if err != nil || !reflect.DeepEqual(queued, replay) {
		t.Fatal("rewrite replay changed job")
	}
	runDramaTicks(t, svc, project, 6)
	detail, err := svc.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Segment.PromptStatus != model.DramaPromptReview || detail.Segment.CandidateID == "" || detail.Segment.CandidateShotRevision != request.ExpectedShotRevision {
		t.Fatalf("candidate not bound %+v", detail.Segment)
	}
	job, err := svc.DramaJobDetail("usr-drama", project, queued["jobId"].(string))
	if err != nil || job.Job.TaskID == "" {
		t.Fatalf("no dispatched task %+v %v", job, err)
	}
	request.IdempotencyKey = "adopt"
	request.ExpectedSegmentRevision = detail.Segment.Revision
	request.CandidateID = detail.Segment.CandidateID
	request.ExpectedSeconds = detail.Segment.Request.Seconds
	request.Model = &detail.Segment.VideoModel
	adopted, err := svc.MutateDramaSegmentPrompt("usr-drama", "adopt", request)
	if err != nil || adopted["promptStatus"] != "ready" {
		t.Fatalf("adopt %v %v", adopted, err)
	}
	detail, err = svc.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "manual"
	request.ExpectedSegmentRevision = detail.Segment.Revision
	request.Prompt = "陈启内心独白，人物不张嘴。"
	request.CandidateID = ""
	edit, err := svc.MutateDramaSegmentPrompt("usr-drama", "edit", request)
	if err != nil || edit["promptStatus"] != model.DramaPromptConflict {
		t.Fatalf("manual %v %v", edit, err)
	}
	detail, err = svc.DramaSegmentDetail("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "confirm"
	request.ExpectedSegmentRevision = detail.Segment.Revision
	request.ExpectedSeconds++
	if _, err := svc.MutateDramaSegmentPrompt("usr-drama", "confirm", request); err == nil {
		t.Fatal("wrong duration tier accepted")
	}
	request.ExpectedSeconds--
	confirmed, err := svc.MutateDramaSegmentPrompt("usr-drama", "confirm", request)
	if err != nil || confirmed["promptStatus"] != "ready" {
		t.Fatalf("confirm %v %v", confirmed, err)
	}
	preflight, err := svc.PreviewDramaSegmentGeneration("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	sound := preflight["sound"].(map[string]any)
	if sound["hasAudioTrack"] != nil || sound["hasSpeech"] != nil || sound["speechGenerationGuaranteed"] != false {
		t.Fatal("audio capability misrepresented")
	}
	data, _ := json.Marshal(preflight)
	if !strings.Contains(string(data), "fullPrompt") {
		t.Fatal("missing submitted prompt")
	}
	if err := svc.repo.UpdateDramaSegmentFields(detail.Segment.ID, map[string]any{"prompt_seconds": 99}); err != nil {
		t.Fatal(err)
	}
	preflight, err = svc.PreviewDramaSegmentGeneration("usr-drama", project, episode, 0)
	if err != nil {
		t.Fatal(err)
	}
	if preflight["canSubmit"] != false || !strings.Contains(strings.Join(preflight["blockers"].([]string), "；"), "时长档位") {
		t.Fatal("preflight missed submission duration rejection")
	}
}
