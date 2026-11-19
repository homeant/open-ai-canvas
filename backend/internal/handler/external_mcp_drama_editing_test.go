package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"yingce/backend/internal/model"
)

func TestExternalMCPDramaEditingDiscoveryAndStructuredErrors(t *testing.T) {
	svc := externalAccessTestService(t)
	server := externalMCPServer(svc, &model.User{ID: "owner"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"get_episode_source": false, "update_episode_source": false, "edit_episode_shots": false, "rewrite_segment_prompt": false, "adopt_segment_prompt": false, "confirm_segment_prompt": false, "preview_segment_generation": false, "inspect_segment_version_media": false, "get_drama_job": false}
	wanted["edit_segment_references"] = false
	for _, name := range []string{"get_segment_audio", "save_segment_audio", "preview_segment_audio", "generate_segment_audio_cue", "bind_segment_audio_cue"} {
		wanted[name] = false
	}
	for _, tool := range list.Tools {
		if _, ok := wanted[tool.Name]; ok {
			wanted[tool.Name] = true
		}
		if tool.Name == "edit_episode_shots" {
			data, _ := json.Marshal(tool.InputSchema)
			var schema struct {
				Properties map[string]struct {
					Items struct {
						Required   []string                   `json:"required"`
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"items"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatal(err)
			}
			item := schema.Properties["edits"].Items
			if item.Properties["durationSeconds"] == nil || item.Properties["dialogueMode"] == nil || item.Properties["sceneCastId"] == nil || item.Properties["castIds"] == nil || item.Properties["propIds"] == nil || item.Properties["lookCastIds"] == nil {
				t.Fatalf("missing flattened edit schema %s", data)
			}
			for _, field := range item.Required {
				if field == "speaker" || field == "durationSeconds" || field == "dialogue" || field == "sceneCastId" || field == "castIds" || field == "propIds" || field == "lookCastIds" {
					t.Fatalf("optional patch field required: %s", field)
				}
			}
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("missing MCP tool %s", name)
		}
	}
	// Invalid input reaches a real MCP call and returns a machine-readable business error.
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "update_episode_source", Arguments: map[string]any{"dramaId": "none", "episodeId": "none", "sourceText": "test", "expectedSourceRevision": "old", "readapt": false, "idempotencyKey": ""}})
	if err != nil || !result.IsError {
		t.Fatalf("expected tool error %+v %v", result, err)
	}
	var problem map[string]any
	if len(result.Content) == 0 {
		t.Fatal("missing error content")
	}
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &problem); err != nil || problem["code"] == nil || problem["reason"] == nil {
		t.Fatalf("unstructured error %+v %v", problem, err)
	}
}
