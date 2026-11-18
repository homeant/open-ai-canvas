package app

import (
	"bytes"
	"encoding/base64"
	"strings"
)

type ExternalPrevisRenderUploadRequest struct {
	FileName       string `json:"fileName"`
	DataBase64     string `json:"dataBase64"`
	Kind           string `json:"kind"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type ExternalPrevisRenderPublishRequest struct {
	DramaID        string `json:"dramaId"`
	EpisodeID      string `json:"episodeId"`
	CanvasID       string `json:"canvasId,omitempty"`
	SceneID        string `json:"sceneId"`
	ShotID         string `json:"shotId"`
	SnapshotHash   string `json:"snapshotHash"`
	ResourceID     string `json:"resourceId"`
	Kind           string `json:"kind"`
	IdempotencyKey string `json:"idempotencyKey"`
	DryRun         bool   `json:"dryRun,omitempty"`
}

func (s *Service) UploadExternalPrevisRender(userID string, req ExternalPrevisRenderUploadRequest) (map[string]any, error) {
	if err := externalPrevisKey(req.IdempotencyKey); err != nil {
		return nil, err
	}
	if err := cloudAgentPrevisValidateText(req.FileName, "预览文件名", 160, true); err != nil {
		return nil, err
	}
	if len(req.DataBase64) > base64.StdEncoding.EncodedLen(externalPrevisMCPModelLimit) {
		return nil, BadAuthRequest("MCP 单次上传最多 2 MiB；大文件请通过 HTTPS 导入资源")
	}
	data, err := base64.StdEncoding.DecodeString(req.DataBase64)
	if err != nil || len(data) == 0 || len(data) > externalPrevisMCPModelLimit {
		return nil, BadAuthRequest("预览 base64 内容无效或过大")
	}
	switch req.Kind {
	case "frame":
		if !strings.HasSuffix(strings.ToLower(req.FileName), ".png") || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			return nil, BadAuthRequest("单帧预览必须是 PNG")
		}
	case "video":
		if !strings.HasSuffix(strings.ToLower(req.FileName), ".webm") || !bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3}) {
			return nil, BadAuthRequest("视频预览必须是 WebM")
		}
	default:
		return nil, BadAuthRequest("kind 只支持 frame 或 video")
	}
	resource, err := s.UploadResourceFile(userID, req.FileName, int64(len(data)), "", 0, 0, 0, bytes.NewReader(data), "mcp-previs-render:"+userID+":"+req.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if err := s.externalPrevisVerifyResourceBytes(userID, resource.ID, data); err != nil {
		return nil, err
	}
	expected := "image"
	if req.Kind == "video" {
		expected = "video"
	}
	if resource.Kind != expected {
		return nil, BadAuthRequest("预览文件媒体类型无效")
	}
	return map[string]any{"resourceId": resource.ID, "kind": req.Kind, "mimeType": resource.MimeType, "bytes": resource.Size}, nil
}

func (s *Service) PublishExternalPrevisRender(userID string, req ExternalPrevisRenderPublishRequest) (map[string]any, error) {
	operation := ExternalPrevisPatchOperation{Type: "shot_render_set", ID: req.ShotID, RenderResourceID: &req.ResourceID, RenderKind: &req.Kind}
	return s.ApplyExternalPrevisPatch(userID, ExternalPrevisPatchRequest{DramaID: req.DramaID, EpisodeID: req.EpisodeID, CanvasID: req.CanvasID, SceneID: req.SceneID, SnapshotHash: req.SnapshotHash, IdempotencyKey: req.IdempotencyKey, Operations: []ExternalPrevisPatchOperation{operation}, DryRun: req.DryRun})
}

func (s *Service) GetExternalPrevisRender(userID string, req ExternalPrevisReadRequest) (map[string]any, error) {
	if req.ShotID == "" {
		return nil, BadAuthRequest("请指定 shotId")
	}
	canvas, err := s.externalPrevisCanvas(userID, ExternalPrevisScope{req.DramaID, req.EpisodeID, req.CanvasID})
	if err != nil {
		return nil, err
	}
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		return nil, err
	}
	scene, ok := findPrevisScene(creationMaps(doc["previsScenes"]), req.SceneID)
	if !ok || (stringValue(scene["episodeId"]) != "" && stringValue(scene["episodeId"]) != req.EpisodeID) {
		return nil, BadAuthRequest("预演场景不存在或属于其他剧集")
	}
	shot, ok := findPrevisShot(scene, req.ShotID)
	if !ok {
		return nil, BadAuthRequest("镜头不存在")
	}
	id := stringValue(shot["renderResourceId"])
	if id == "" {
		return map[string]any{"status": "missing", "sceneId": req.SceneID, "shotId": req.ShotID}, nil
	}
	resource, err := s.Resource(userID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "ready", "sceneId": req.SceneID, "shotId": req.ShotID, "resourceId": id, "kind": stringValue(shot["renderKind"]), "mimeType": resource.MimeType, "bytes": resource.Size}, nil
}
