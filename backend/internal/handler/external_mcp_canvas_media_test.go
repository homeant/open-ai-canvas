package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	"yingce/backend/internal/service"
)

func TestExternalMCPCanvasMediaHTTPSandbox(t *testing.T) {
	root := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "sandbox.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(database.Models()...); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	profile, _ := json.Marshal(service.DefaultModelCapabilityConfigForModel("metaso-h3", "MiniMax-H3"))
	for _, row := range []any{
		&model.User{ID: "user", Username: "sandbox", Status: model.UserStatusActive},
		&model.CanvasProject{ID: "canvas", UserID: "user", PayloadJSON: `{"nodes":[],"connections":[]}`},
		&model.ModelChannel{ID: "channel", Name: "sandbox", Scope: model.ChannelScopeSystem, Enabled: true},
		&model.ChannelModel{ID: "h3", ChannelID: "channel", ModelKey: "MiniMax-H3", Capability: "video", Protocol: "metaso-h3", CapabilityConfigJSON: string(profile), BillingMode: "per_second", PriceConfigured: true, Enabled: true},
		&model.ChannelModelPriceTier{ID: "tier", ChannelModelID: "h3", SelectorKey: "{}", SelectorJSON: "{}", BillingMode: "per_second", UnitPriceMicrocredits: 10, PriceConfigured: true, Enabled: true},
		&model.CreditAccount{UserID: "user", AvailableMicrocredits: 10000},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := service.New(repository.New(db), root)
	ConfigureRuntime(svc)
	defer ConfigureRuntime(nil)
	key, err := svc.CreateUserAPIKey("user", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	RegisterExternalMCPRoutes(router.Group("/api"), svc)
	httpServer := httptest.NewServer(router)
	defer httpServer.Close()
	t.Setenv("CANVAS_MCP_PUBLIC_ORIGIN", httpServer.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "canvas-sandbox", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/api/mcp", HTTPClient: &http.Client{Transport: audioSandboxAuthTransport{token: key.Token}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"begin_media_upload": false, "upload_media_chunk": false, "complete_media_upload": false, "list_canvas_media_nodes": false, "get_canvas_media_node": false, "add_canvas_media_node": false, "connect_canvas_nodes": false, "create_canvas_video_node": false, "configure_canvas_video_node": false, "preview_canvas_generation": false, "submit_canvas_generation": false, "get_canvas_generation": false, "sync_canvas_generation": false, "bind_shot_reference_images": false, "get_segment_audio": false, "edit_segment_references": false}
	for _, tool := range list.Tools {
		if _, ok := wanted[tool.Name]; ok {
			wanted[tool.Name] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatal("missing tool", name)
		}
	}
	call := func(name string, args map[string]any) map[string]any {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || r.IsError {
			t.Fatalf("%s %+v %v", name, r, err)
		}
		var value map[string]any
		raw, _ := json.Marshal(r.StructuredContent)
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 32, 48))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if bun, lookupErr := exec.LookPath("bun"); lookupErr == nil {
		file := filepath.Join(root, "cli.png")
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, bun, "../../../scripts/yingce-mcp.mjs", "upload", file, "image", "cli-upload")
		command.Env = append(os.Environ(), "YINGCE_MCP_URL="+httpServer.URL+"/api/mcp", "YINGCE_MCP_TOKEN="+key.Token)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("direct MCP upload %v: %s", err, out)
		}
		var uploaded map[string]any
		if err = json.Unmarshal(out, &uploaded); err != nil || uploaded["adopted"] != false {
			t.Fatalf("CLI result %v", err)
		}
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	begin := call("begin_media_upload", map[string]any{"fileName": "sandbox.png", "kind": "image", "size": len(data), "sha256": hash, "idempotencyKey": "upload"})
	call("upload_media_chunk", map[string]any{"uploadId": begin["uploadId"], "index": 0, "dataBase64": base64.StdEncoding.EncodeToString(data), "sha256": hash})
	complete := call("complete_media_upload", map[string]any{"uploadId": begin["uploadId"]})
	resourceID := complete["resource"].(map[string]any)["id"]
	graph := call("list_canvas_media_nodes", map[string]any{"canvasId": "canvas"})
	added := call("add_canvas_media_node", map[string]any{"canvasId": "canvas", "snapshotHash": graph["snapshotHash"], "idempotencyKey": "review", "resourceId": resourceID, "title": "独立上传审片"})
	draft := call("create_canvas_video_node", map[string]any{"canvasId": "canvas", "snapshotHash": added["snapshotHash"], "idempotencyKey": "draft", "title": "H3独立候选", "prompt": "手机实体信息特写", "channelId": "channel", "channelModelKey": "MiniMax-H3", "durationSeconds": 4, "size": "9:16", "resolution": "768P", "referenceNodeIds": []string{added["nodeId"].(string)}})
	preview := call("preview_canvas_generation", map[string]any{"canvasId": "canvas", "nodeId": draft["nodeId"], "snapshotHash": draft["snapshotHash"]})
	if preview["amountMicrocredits"] != float64(40) || preview["orderedReferences"].([]any)[0].(map[string]any)["resourceId"] != resourceID {
		t.Fatalf("bad preflight %+v", preview)
	}
	args := map[string]any{"canvasId": "canvas", "nodeId": draft["nodeId"], "snapshotHash": draft["snapshotHash"], "expectedPreviewHash": preview["previewHash"], "maxCostMicrocredits": 40, "idempotencyKey": "submit"}
	submitted := call("submit_canvas_generation", args)
	replay := call("submit_canvas_generation", args)
	if submitted["taskId"] != replay["taskId"] {
		t.Fatal("MCP retry duplicated task")
	}
	read := call("get_canvas_generation", map[string]any{"canvasId": "canvas", "nodeId": draft["nodeId"]})
	if read["status"] != "queued" {
		t.Fatalf("queue %+v", read)
	}
	var count int64
	db.Model(&model.Task{}).Count(&count)
	if count != 1 {
		t.Fatal("unexpected task count", count)
	}
}
