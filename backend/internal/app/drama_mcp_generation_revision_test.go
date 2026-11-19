package app

import (
	"errors"
	"testing"

	"yingce/backend/internal/model"
)

func TestDramaMCPGenerationChecksIndependentRevisions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prompt   *int64
		segment  map[string]int64
		conflict bool
	}{
		{"prompt_only", ptr(int64(4)), nil, false},
		{"segment_only", nil, map[string]int64{"0": 61}, false},
		{"both_current", ptr(int64(4)), map[string]int64{"0": 61}, false},
		{"stale_segment_is_not_overwritten", ptr(int64(4)), map[string]int64{"0": 60}, true},
		{"stale_prompt", ptr(int64(3)), map[string]int64{"0": 61}, true},
		{"missing_versions", nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, project, episode, before := mcpEditingFixture(t)
			if err := svc.repo.UpdateDramaSegmentFields(before.Segment.ID, map[string]any{"revision": 61, "prompt_revision": 4}); err != nil {
				t.Fatal(err)
			}
			initialVideos := len(dramaVideoRequests(fake))
			request := GenerateDramaMCPRequest{DramaID: project, EpisodeID: episode, IdempotencyKey: "version-check", ExpectedSegmentRevisions: tc.segment, Request: GenerateDramaSegmentsRequest{Segments: []int{0}, ExpectedPromptRevision: tc.prompt}}
			_, err := svc.GenerateDramaSegmentsMCP("usr-drama", request)
			row, readErr := svc.repo.DramaSegment(project, before.Segment.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.conflict {
				var conflict *AppError
				if !errors.As(err, &conflict) || conflict.Status != 409 {
					t.Fatalf("expected conflict, got %v", err)
				}
				state := conflict.Details
				if state["segmentRevision"] != int64(61) || state["promptRevision"] != int64(4) || state["shotRevision"] != before.Segment.ShotRevision || state["promptStatus"] == nil {
					t.Fatalf("missing actual versions: %#v", conflict.Details)
				}
				var receipts int64
				if err := fake.db.Model(&model.DramaMutationReceipt{}).Count(&receipts).Error; err != nil || receipts != 0 {
					t.Fatalf("failed request persisted receipt: %d %v", receipts, err)
				}
				if row.Revision != 61 || row.VideoStatus != before.Segment.VideoStatus {
					t.Fatal("conflict changed segment")
				}
			} else if err != nil || row.VideoStatus != model.DramaMediaQueued {
				t.Fatalf("current versions did not queue: %+v %v", row, err)
			}
			if len(dramaVideoRequests(fake)) != initialVideos {
				t.Fatal("request submitted upstream synchronously")
			}
		})
	}
}

func TestDramaHTTPGenerationUsesPromptRevision(t *testing.T) {
	svc, _, project, episode, before := mcpEditingFixture(t)
	if err := svc.repo.UpdateDramaSegmentFields(before.Segment.ID, map[string]any{"revision": 61, "prompt_revision": 4}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.GenerateDramaSegments("usr-drama", project, episode, GenerateDramaSegmentsRequest{Segments: []int{0}, ExpectedPromptRevision: ptr(int64(3))})
	var conflict *AppError
	if !errors.As(err, &conflict) || conflict.Status != 409 {
		t.Fatalf("stale prompt not rejected: %v", err)
	}
	if conflict.Details["promptRevision"] != int64(4) {
		t.Fatal("HTTP conflict missing current prompt revision")
	}
	if _, err := svc.GenerateDramaSegments("usr-drama", project, episode, GenerateDramaSegmentsRequest{Segments: []int{0}, ExpectedPromptRevision: ptr(int64(4))}); err != nil {
		t.Fatal(err)
	}
}
