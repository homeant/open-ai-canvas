package protocol

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestMetasoH3ReferenceReleaseDeclaresActualDeliveredImageSlots(t *testing.T) {
	adapter := officialPackageAdapter(t, "metaso-h3.yingce-plugin", "metaso-h3")
	if adapter.Metadata().Version != "1.0.4" {
		t.Fatalf("reference contract must ship as a new immutable release: %s", adapter.Metadata().Version)
	}
	rule := adapter.Metadata().VideoReferences
	if rule == nil || rule.Layout != "content" || rule.Token != "参考图{n}" {
		t.Fatalf("reference numbering contract missing: %+v", rule)
	}
	request := GenerationRequest{Model: "MiniMax-H3", Prompt: "参考图1中的人物保持参考图2的道具", Operation: "reference_to_video", Duration: 4,
		Images: []MediaReference{{ID: "prop", URL: "https://example.com/prop.png", Role: "reference_image", Order: 2}, {ID: "person", URL: "https://example.com/person.png", Role: "reference_image", Order: 1}}}
	spec, err := adapter.BuildCreate(context.Background(), RequestContext{Request: request})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := rule.Bind(request, &spec)
	if err != nil || bound.Tokens["prop"] != "参考图1" || bound.Tokens["person"] != "参考图2" {
		t.Fatalf("actual image order not bound to prompt: %+v %v", bound, err)
	}
	body, err := json.Marshal(spec.Body)
	if err != nil || !strings.Contains(string(body), "person.png") || !strings.Contains(string(body), "prop.png") || strings.Index(string(body), "prop.png") > strings.Index(string(body), "person.png") {
		t.Fatalf("ordered images absent from upstream payload: %s %v", body, err)
	}
}

func TestMetasoH3VideoDurationRange(t *testing.T) {
	adapter := officialPackageAdapter(t, "metaso-h3.yingce-plugin", "metaso-h3")
	for seconds := 4; seconds <= 30; seconds++ {
		t.Run(strconv.Itoa(seconds), func(t *testing.T) {
			spec, err := adapter.BuildCreate(context.Background(), RequestContext{Request: GenerationRequest{Model: "MiniMax-H3", Prompt: "a clip", Duration: seconds}})
			if err != nil {
				t.Fatal(err)
			}
			if spec.Path != "/api/minimax/v2/video_generation" {
				t.Fatalf("create path = %q", spec.Path)
			}
			if !spec.OriginPath {
				t.Fatal("Metaso create path must be relative to the origin")
			}
			if duration := manifestTestBody(t, spec)["duration"]; duration != float64(seconds) {
				t.Fatalf("upstream duration = %#v, want %d", duration, seconds)
			}
		})
	}
	poll, err := adapter.BuildPoll(context.Background(), PollContext{TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if poll.Path != "/api/minimax/v2/query/video_generation/task-1" || !poll.OriginPath {
		t.Fatalf("poll path = %q, originPath = %t", poll.Path, poll.OriginPath)
	}
}
