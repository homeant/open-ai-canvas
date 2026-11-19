package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

type ExternalPrevisRenderRequest struct {
	DramaID        string   `json:"dramaId,omitempty"`
	EpisodeID      string   `json:"episodeId,omitempty"`
	CanvasID       string   `json:"canvasId,omitempty"`
	SceneID        string   `json:"sceneId"`
	ShotID         string   `json:"shotId"`
	SnapshotHash   string   `json:"snapshotHash"`
	IdempotencyKey string   `json:"idempotencyKey"`
	Time           *float64 `json:"time,omitempty"`
	Duration       *float64 `json:"duration,omitempty"`
	FPS            *int     `json:"fps,omitempty"`
	LongEdge       *int     `json:"longEdge,omitempty"`
	Kind           string   `json:"-"`
}

var externalPrevisRenderSlots = make(chan struct{}, 2)

func externalPrevisRenderScript() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CANVAS_PREVIS_RENDER_SCRIPT")); configured != "" {
		return configured, nil
	}
	for _, candidate := range []string{"agent-runtime/pi/previs-render.mjs", filepath.Join("..", "..", "agent-runtime/pi/previs-render.mjs"), "/app/backend/agent-runtime/pi/previs-render.mjs"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", BadAuthRequest("服务端预演渲染脚本未安装")
}

func (s *Service) externalPrevisInlineResource(userID, resourceID string, limit int64) (string, error) {
	resource, body, err := s.OpenResource(userID, resourceID)
	if err != nil {
		return "", err
	}
	defer body.Close()
	if resource.Size <= 0 || resource.Size > limit {
		return "", BadAuthRequest("预演模型资源过大")
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) != resource.Size {
		return "", BadAuthRequest("预演模型资源不完整")
	}
	return "data:" + resource.MimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (s *Service) RenderExternalPrevis(ctx context.Context, userID string, req ExternalPrevisRenderRequest) (map[string]any, error) {
	if err := externalPrevisKey(req.IdempotencyKey); err != nil {
		return nil, err
	}
	if req.Kind != "frame" && req.Kind != "video" {
		return nil, BadAuthRequest("预览类型无效")
	}
	if req.LongEdge != nil && (*req.LongEdge < 320 || *req.LongEdge > 1280) {
		return nil, BadAuthRequest("longEdge 必须在 320 到 1280 之间")
	}
	if req.Time != nil && (*req.Time < 0 || *req.Time > 60) {
		return nil, BadAuthRequest("time 必须在 0 到 60 秒之间")
	}
	if req.Duration != nil && (*req.Duration < 0.1 || *req.Duration > 10) {
		return nil, BadAuthRequest("视频时长不能超过 10 秒")
	}
	if req.FPS != nil && (*req.FPS < 1 || *req.FPS > 30) {
		return nil, BadAuthRequest("视频帧率不能超过 30")
	}
	canvas, err := s.externalPrevisCanvas(userID, ExternalPrevisScope{req.DramaID, req.EpisodeID, req.CanvasID})
	if err != nil {
		return nil, err
	}
	mutationID := "mcp-previs-render:" + externalPrevisDigest(req.IdempotencyKey)
	digest := externalPrevisDigest([]any{req.Kind, req.DramaID, req.EpisodeID, canvas.ID, req.SceneID, req.ShotID, req.SnapshotHash, req.Time, req.Duration, req.FPS, req.LongEdge})
	previous, err := s.repo.CanvasMutationReceiptForUser(userID, canvas.ID, mutationID)
	if err == nil {
		if previous.PayloadDigest != digest {
			return nil, creationConflict("同一幂等键对应的预演渲染请求不同")
		}
		if previous.ResultJSON == "" {
			return nil, creationConflict("预演渲染仍在进行")
		}
		var replay map[string]any
		if err := json.Unmarshal([]byte(previous.ResultJSON), &replay); err != nil {
			return nil, err
		}
		return replay, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	scene, _, err := s.externalPrevisRenderScene(userID, canvas, req)
	if err != nil {
		return nil, err
	}
	scene["activeShotId"] = req.ShotID
	longEdge := 960
	if req.Kind == "video" {
		longEdge = 720
	}
	if req.LongEdge != nil {
		longEdge = *req.LongEdge
	}
	payload := map[string]any{"scene": scene, "shotId": req.ShotID, "kind": req.Kind, "longEdge": longEdge}
	if req.Time != nil {
		payload["time"] = *req.Time
	}
	if req.Duration != nil {
		payload["duration"] = *req.Duration
	}
	if req.FPS != nil {
		payload["fps"] = *req.FPS
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	select {
	case externalPrevisRenderSlots <- struct{}{}:
		defer func() { <-externalPrevisRenderSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	renderCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := externalPrevisRunBrowser(renderCtx, raw)
	if err != nil {
		return nil, err
	}
	var rendered struct {
		DataBase64 string `json:"dataBase64"`
		MimeType   string `json:"mimeType"`
		Bytes      int    `json:"bytes"`
	}
	if err := json.Unmarshal(output, &rendered); err != nil {
		return nil, BadAuthRequest("浏览器渲染结果无效")
	}
	data, err := base64.StdEncoding.DecodeString(rendered.DataBase64)
	if err != nil || len(data) == 0 || len(data) > 32<<20 || rendered.Bytes != len(data) {
		return nil, BadAuthRequest("浏览器渲染文件无效或过大")
	}
	name := "previs-frame.png"
	expected := "image"
	if req.Kind == "video" {
		name = "previs-video.webm"
		expected = "video"
	}
	resource, err := s.UploadResourceFile(userID, name, int64(len(data)), expected, 0, 0, 0, bytes.NewReader(data), "mcp-previs-headless:"+userID+":"+req.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if resource.Kind != expected {
		return nil, BadAuthRequest("渲染文件类型无效")
	}
	published, err := s.PublishExternalPrevisRender(userID, ExternalPrevisRenderPublishRequest{DramaID: req.DramaID, EpisodeID: req.EpisodeID, CanvasID: canvas.ID, SceneID: req.SceneID, ShotID: req.ShotID, SnapshotHash: req.SnapshotHash, ResourceID: resource.ID, Kind: req.Kind, IdempotencyKey: "render-publish:" + req.IdempotencyKey})
	if err != nil {
		return nil, err
	}
	result := map[string]any{"status": "ready", "canvasId": canvas.ID, "sceneId": req.SceneID, "shotId": req.ShotID, "time": numberValue(payload["time"], 0), "resourceId": resource.ID, "kind": req.Kind, "mimeType": resource.MimeType, "bytes": resource.Size, "snapshotHash": published["snapshotHash"], "canvasSnapshotHash": published["canvasSnapshotHash"]}
	err = s.repo.Transaction(func(tx *repository.Repository) error {
		receipt := &model.CanvasMutationReceipt{ID: newID(), UserID: userID, CanvasID: canvas.ID, MutationID: mutationID, PayloadDigest: digest}
		created, err := tx.TryCreateCanvasMutationReceipt(receipt)
		if err != nil {
			return err
		}
		if !created {
			stored, err := tx.CanvasMutationReceiptForUser(userID, canvas.ID, mutationID)
			if err != nil {
				return err
			}
			if stored.PayloadDigest != digest {
				return creationConflict("同一幂等键对应的预演渲染请求不同")
			}
			return nil
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		receipt.ResultJSON = string(raw)
		return tx.FinalizeCanvasMutationReceipt(receipt)
	})
	return result, err
}

func externalPrevisRunBrowser(ctx context.Context, input []byte) ([]byte, error) {
	if configured := strings.TrimSpace(os.Getenv("CANVAS_PREVIS_RENDERER_URL")); configured != "" {
		parsed, err := url.Parse(configured)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, BadAuthRequest("服务端预演渲染器地址无效")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, configured, bytes.NewReader(input))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 48<<20))
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			return nil, BadAuthRequest("服务端预演渲染失败：" + truncateRunes(string(body), 240))
		}
		return body, nil
	}
	script, err := externalPrevisRenderScript()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Stdin = bytes.NewReader(input)
	var output, stderr bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, BadAuthRequest("服务端预演渲染超时")
		}
		return nil, BadAuthRequest("服务端预演渲染失败：" + truncateRunes(strings.TrimSpace(stderr.String()), 240))
	}
	return output.Bytes(), nil
}

func (s *Service) externalPrevisRenderScene(userID string, canvas *model.CanvasProject, req ExternalPrevisRenderRequest) (map[string]any, map[string]any, error) {
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		return nil, nil, err
	}
	scene, ok := findPrevisScene(creationMaps(doc["previsScenes"]), req.SceneID)
	if !ok || (req.EpisodeID != "" && stringValue(scene["episodeId"]) != "" && stringValue(scene["episodeId"]) != req.EpisodeID) {
		return nil, nil, BadAuthRequest("预演场景不存在或属于其他剧集")
	}
	if req.SnapshotHash == "" || creationHash(scene) != req.SnapshotHash {
		return nil, nil, creationConflict("预演场景已变化，请重新读取 snapshotHash")
	}
	shot, ok := findPrevisShot(scene, req.ShotID)
	if !ok {
		return nil, nil, BadAuthRequest("预演镜头不存在")
	}
	if req.Kind == "video" && req.Duration == nil && numberValue(shot["duration"], 0) > 10 {
		return nil, nil, BadAuthRequest("视频时长不能超过 10 秒")
	}
	if req.Kind == "video" && req.FPS == nil && numberValue(shot["fps"], 24) > 30 {
		return nil, nil, BadAuthRequest("视频帧率不能超过 30")
	}
	if req.Time != nil && *req.Time > numberValue(shot["duration"], 5) {
		return nil, nil, BadAuthRequest("单帧时间不能超过镜头时长")
	}
	if err := cloudAgentPrevisValidateScene(scene); err != nil {
		return nil, nil, err
	}
	resourceURLs := map[string]string{}
	var total int64
	shotVisibility := map[string]bool{}
	for _, state := range creationMaps(shot["objectStates"]) {
		if visible, ok := state["visible"].(bool); ok {
			shotVisibility[stringValue(state["objectId"])] = visible
		}
	}
	for _, object := range creationMaps(scene["objects"]) {
		visible := boolValue(object["visible"], true)
		if override, found := shotVisibility[stringValue(object["id"])]; found {
			visible = override
		}
		if !visible {
			continue
		}
		if stringValue(object["url"]) == "" && stringValue(object["storageKey"]) == "" {
			continue
		}
		resourceID := stringValue(object["resourceId"])
		if resourceID == "" {
			resourceID = strings.TrimPrefix(stringValue(object["storageKey"]), "resource:")
		}
		if resourceID == "" || resourceID == stringValue(object["storageKey"]) {
			return nil, nil, BadAuthRequest("预演模型必须使用当前用户的资源 ID")
		}
		if resourceURLs[resourceID] == "" {
			resource, err := s.Resource(userID, resourceID)
			if err != nil {
				return nil, nil, err
			}
			total += resource.Size
			if total > 32<<20 {
				return nil, nil, BadAuthRequest("预演场景模型总量超过 32 MiB")
			}
			url, err := s.externalPrevisInlineResource(userID, resourceID, 8<<20)
			if err != nil {
				return nil, nil, err
			}
			resourceURLs[resourceID] = url
		}
		object["url"] = resourceURLs[resourceID]
		delete(object, "storageKey")
	}
	return scene, shot, nil
}
