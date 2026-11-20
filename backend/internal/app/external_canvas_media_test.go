package app

import (
	"encoding/json"
	"strings"
	"testing"

	"gorm.io/gorm"
	"yingce/backend/internal/model"
)

func externalCanvasFixture(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	s, db, _ := agentMediaFixture(t)
	profile := DefaultModelCapabilityConfigForModel("metaso-h3", "MiniMax-H3")
	for _, row := range []any{
		&model.ChannelModel{ID: "h3", ChannelID: "channel", ModelKey: "MiniMax-H3", Capability: "video", Protocol: "metaso-h3", CapabilityConfigJSON: mustEncodeModelCapabilityConfig(t, profile), BillingMode: "per_second", PriceConfigured: true, Enabled: true},
		&model.ChannelModelPriceTier{ID: "h3-price", ChannelModelID: "h3", SelectorKey: "{}", SelectorJSON: "{}", BillingMode: "per_second", UnitPriceMicrocredits: 10, PriceConfigured: true, Enabled: true},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	features := defaultFeatureAvailability()
	features.VideoWorkbenchEnabled = false
	saveAdmissionFeatures(t, s, features)
	return s, db
}
func externalCanvasSnapshot(t *testing.T, s *Service) string {
	t.Helper()
	read, err := s.ExternalCanvasMedia("user", ExternalCanvasReadRequest{CanvasID: "agent-canvas"})
	if err != nil {
		t.Fatal(err)
	}
	return read["snapshotHash"].(string)
}
func externalVideoDraft(t *testing.T, s *Service, refs []string) string {
	t.Helper()
	draft, err := s.ConfigureExternalCanvasVideo("user", ExternalCanvasVideoRequest{ExternalCanvasMutation: ExternalCanvasMutation{CanvasID: "agent-canvas", SnapshotHash: externalCanvasSnapshot(t, s), IdempotencyKey: "draft"}, Title: "H3隔离候选", Prompt: "真实手机信息特写，无漂浮字卡", ChannelID: "channel", ChannelModelKey: "MiniMax-H3", DurationSeconds: 4, Size: "9:16", Resolution: "768P", ReferenceNodeIDs: refs})
	if err != nil {
		t.Fatal(err)
	}
	return draft["nodeId"].(string)
}

func TestExternalCanvasH3PreviewSubmitAndSync(t *testing.T) {
	s, db := externalCanvasFixture(t)
	id := externalVideoDraft(t, s, []string{"cat", "hero"})
	snapshot := externalCanvasSnapshot(t, s)
	preview, err := s.PreviewExternalCanvasGeneration("user", ExternalCanvasGenerationRequest{CanvasID: "agent-canvas", NodeID: id, SnapshotHash: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if preview["options"].(map[string]any)["size"] != "9:16" || stringValue(preview["options"].(map[string]any)["videoSeconds"]) != "4" || preview["operation"] != "reference_to_video" || preview["amountMicrocredits"] != int64(40) {
		t.Fatalf("bad preview %+v", preview)
	}
	refs := preview["orderedReferences"].([]map[string]any)
	if preview["audioOutputMode"] != "native" {
		t.Fatalf("H3 native audio lost: %+v", preview)
	}
	if len(refs) != 2 || refs[0]["resourceId"] != "ref-one" || refs[1]["resourceId"] != "ref-two" || refs[1]["inputOrder"] != 1 {
		t.Fatalf("bad refs %+v", refs)
	}
	var count int64
	db.Model(&model.Task{}).Count(&count)
	if count != 0 {
		t.Fatal("preview queued work")
	}
	account, _ := s.repo.CreditAccount("user")
	if account.AvailableMicrocredits != 10000 || account.ReservedMicrocredits != 0 {
		t.Fatal("preview charged")
	}
	if externalCanvasSnapshot(t, s) != snapshot {
		t.Fatal("preview changed canvas")
	}
	in := ExternalCanvasSubmitRequest{ExternalCanvasMutation: ExternalCanvasMutation{CanvasID: "agent-canvas", SnapshotHash: snapshot, IdempotencyKey: "submit"}, NodeID: id, ExpectedPreviewHash: preview["previewHash"].(string), MaxCostMicrocredits: 39}
	if _, err = s.SubmitExternalCanvasGeneration("user", in); err == nil {
		t.Fatal("ignored ceiling")
	}
	in.MaxCostMicrocredits = 40
	submitted, err := s.SubmitExternalCanvasGeneration("user", in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SubmitExternalCanvasGeneration("user", in)
	if err != nil || again["taskId"] != submitted["taskId"] {
		t.Fatalf("replay %+v %v", again, err)
	}
	taskID := submitted["taskId"].(string)
	db.Model(&model.Task{}).Count(&count)
	if count != 1 {
		t.Fatalf("queued %d", count)
	}
	db.Model(&model.BillingOrder{}).Count(&count)
	if count != 1 {
		t.Fatalf("orders %d", count)
	}
	db.Model(&model.CloudAgentExecution{}).Count(&count)
	if count != 0 {
		t.Fatal("invented Agent approval")
	}
	task, _ := s.repo.Task(taskID)
	if task.AgentRunID != "" || task.WorkbenchKind != "" {
		t.Fatal("wrong task entry point")
	}
	var input map[string]any
	if err = json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if input["metadata"].(map[string]any)["source"] != "mcp_canvas_generation" {
		t.Fatal("source missing")
	}
	account, _ = s.repo.CreditAccount("user")
	if account.ReservedMicrocredits != 40 {
		t.Fatalf("reserve %+v", account)
	}
	// Synthetic completion isolates writeback checks from paid providers; the
	// separate worker test exercises actual H3 HTTP and output storage.
	if err = db.Create(&model.Resource{ID: "finished", UserID: "user", Kind: "video", Status: "ready", MimeType: "video/mp4", Width: 180, Height: 320, DurationMs: 4000}).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]any{"status": model.TaskStatusSucceeded, "result_json": `{"mode":"video","video":{"storageKey":"resource:finished"}}`}).Error; err != nil {
		t.Fatal(err)
	}
	read, err := s.GetExternalCanvasGeneration("user", "agent-canvas", id)
	if err != nil || read["resource"].(map[string]any)["id"] != "finished" {
		t.Fatalf("read %+v %v", read, err)
	}
	sync := ExternalCanvasSyncRequest{ExternalCanvasMutation: ExternalCanvasMutation{CanvasID: "agent-canvas", SnapshotHash: externalCanvasSnapshot(t, s), IdempotencyKey: "sync"}, NodeID: id, TaskID: taskID}
	if _, err = s.SyncExternalCanvasGeneration("user", sync); err != nil {
		t.Fatal(err)
	}
	graph, err := s.ExternalCanvasMedia("user", ExternalCanvasReadRequest{CanvasID: "agent-canvas", NodeID: id})
	if err != nil {
		t.Fatal(err)
	}
	node := graph["nodes"].([]map[string]any)[0]
	if node["status"] != "success" || node["resource"].(map[string]any)["id"] != "finished" || node["adopted"] != false || node["qaStatus"] != "unreviewed" {
		t.Fatalf("writeback %+v", node)
	}
	if _, err = s.ExternalCanvasMedia("other", ExternalCanvasReadRequest{CanvasID: "agent-canvas"}); err == nil {
		t.Fatal("cross-account read")
	}
}

func TestExternalCanvasReviewAndPersistedConnections(t *testing.T) {
	s, db := externalCanvasFixture(t)
	scope := ExternalCanvasMutation{CanvasID: "agent-canvas", SnapshotHash: externalCanvasSnapshot(t, s), IdempotencyKey: "review"}
	in := ExternalCanvasMediaNodeRequest{ExternalCanvasMutation: scope, ResourceID: "ref-one", Title: "上传图待审片"}
	node, err := s.AddExternalCanvasMediaNode("user", in)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.AddExternalCanvasMediaNode("user", in); err != nil || replay["nodeId"] != node["nodeId"] {
		t.Fatal("add replay", err)
	}
	imageID := node["nodeId"].(string)
	id := externalVideoDraft(t, s, []string{})
	connect := ExternalCanvasConnectRequest{ExternalCanvasMutation: ExternalCanvasMutation{CanvasID: "agent-canvas", SnapshotHash: externalCanvasSnapshot(t, s), IdempotencyKey: "connect"}, FromNodeID: imageID, ToNodeID: id}
	if _, err = s.ConnectExternalCanvasNodes("user", connect); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewExternalCanvasGeneration("user", ExternalCanvasGenerationRequest{CanvasID: "agent-canvas", NodeID: id, SnapshotHash: externalCanvasSnapshot(t, s)})
	if err != nil {
		t.Fatal(err)
	}
	if preview["orderedReferences"].([]map[string]any)[0]["resourceId"] != "ref-one" {
		t.Fatal("ignored real edge")
	}
	// A prompt mention cannot create a missing edge in a read-only preview.
	canvas, _ := s.repo.CanvasProjectForUser("user", "agent-canvas")
	doc, _ := creationDocument(canvas.PayloadJSON)
	nodes, _ := creationObjects(doc["nodes"])
	nodes[id]["metadata"].(map[string]any)["prompt"] = "只参考 @[node:hero]"
	data, _ := json.Marshal(doc)
	if err = db.Model(&model.CanvasProject{}).Where("id = ?", "agent-canvas").Update("payload_json", string(data)).Error; err != nil {
		t.Fatal(err)
	}
	_, err = s.PreviewExternalCanvasGeneration("user", ExternalCanvasGenerationRequest{CanvasID: "agent-canvas", NodeID: id, SnapshotHash: externalCanvasSnapshot(t, s)})
	if err == nil {
		t.Fatal("mention fabricated edge")
	}
	var count int64
	db.Model(&model.Task{}).Count(&count)
	if count != 0 {
		t.Fatal("unconnected mention charged")
	}
	if _, err = s.PreviewExternalCanvasGeneration("user", ExternalCanvasGenerationRequest{CanvasID: "agent-canvas", NodeID: id, SnapshotHash: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("stale snapshot accepted")
	}
}
