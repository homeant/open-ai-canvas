package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"time"
)

type ExternalPrevisSampleRequest struct {
	CanvasID     string    `json:"canvasId"`
	SceneID      string    `json:"sceneId"`
	ShotID       string    `json:"shotId"`
	SnapshotHash string    `json:"snapshotHash"`
	Times        []float64 `json:"times"`
	LongEdge     *int      `json:"longEdge,omitempty"`
}

type ExternalPrevisSampleFrame struct {
	Time        float64        `json:"time"`
	MimeType    string         `json:"mimeType"`
	Bytes       int            `json:"bytes"`
	Diagnostics map[string]any `json:"diagnostics"`
	PNG         []byte         `json:"-"`
}

type ExternalPrevisSamples struct {
	CanvasID     string                      `json:"canvasId"`
	SceneID      string                      `json:"sceneId"`
	ShotID       string                      `json:"shotId"`
	SnapshotHash string                      `json:"snapshotHash"`
	ReadOnly     bool                        `json:"readOnly"`
	Frames       []ExternalPrevisSampleFrame `json:"frames"`
	Warnings     []map[string]any            `json:"warnings"`
}

func (s *Service) SampleExternalPrevisFrames(ctx context.Context, userID string, req ExternalPrevisSampleRequest) (*ExternalPrevisSamples, error) {
	if len(req.Times) < 1 || len(req.Times) > 8 {
		return nil, BadAuthRequest("times 需要 1–8 个递增的镜头内时间点")
	}
	edge := 640
	if req.LongEdge != nil {
		edge = *req.LongEdge
	}
	if edge < 320 || edge > 960 {
		return nil, BadAuthRequest("批量取样 longEdge 必须为 320–960")
	}
	canvas, err := s.externalPrevisCanvas(userID, ExternalPrevisScope{CanvasID: req.CanvasID})
	if err != nil {
		return nil, err
	}
	scene, shot, err := s.externalPrevisRenderScene(userID, canvas, ExternalPrevisRenderRequest{SceneID: req.SceneID, ShotID: req.ShotID, SnapshotHash: req.SnapshotHash, Kind: "frames"})
	if err != nil {
		return nil, err
	}
	previous := -1.0
	for _, at := range req.Times {
		if math.IsNaN(at) || math.IsInf(at, 0) || at < 0 || at > numberValue(shot["duration"], 5) || at <= previous {
			return nil, BadAuthRequest("取样时间须递增且位于镜头时长内")
		}
		previous = at
	}
	input, err := json.Marshal(map[string]any{"scene": scene, "shotId": req.ShotID, "kind": "frames", "times": req.Times, "longEdge": edge})
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
	output, err := externalPrevisRunBrowser(renderCtx, input)
	if err != nil {
		return nil, err
	}
	var result struct {
		Frames []struct {
			Time        float64        `json:"time"`
			MimeType    string         `json:"mimeType"`
			Bytes       int            `json:"bytes"`
			DataBase64  string         `json:"dataBase64"`
			Diagnostics map[string]any `json:"diagnostics"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(output, &result); err != nil || len(result.Frames) != len(req.Times) {
		return nil, BadAuthRequest("浏览器批量取样结果不完整")
	}
	samples := &ExternalPrevisSamples{CanvasID: canvas.ID, SceneID: req.SceneID, ShotID: req.ShotID, SnapshotHash: req.SnapshotHash, ReadOnly: true, Warnings: previsAnimationWarnings(scene, req.ShotID)}
	total := 0
	for index, frame := range result.Frames {
		data, err := base64.StdEncoding.DecodeString(frame.DataBase64)
		total += len(data)
		if err != nil || frame.MimeType != "image/png" || frame.Time != req.Times[index] || len(data) != frame.Bytes || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) || len(data) > 4<<20 || total > 24<<20 {
			return nil, BadAuthRequest("批量取样包含无效、错序或过大的图片")
		}
		samples.Frames = append(samples.Frames, ExternalPrevisSampleFrame{Time: frame.Time, MimeType: frame.MimeType, Bytes: frame.Bytes, PNG: data, Diagnostics: frame.Diagnostics})
	}
	current, err := s.repo.CanvasProjectForUser(userID, canvas.ID)
	if err != nil {
		return nil, err
	}
	doc, err := creationDocument(current.PayloadJSON)
	if err != nil {
		return nil, err
	}
	fresh, found := findPrevisScene(creationMaps(doc["previsScenes"]), req.SceneID)
	if !found || creationHash(fresh) != req.SnapshotHash {
		return nil, creationConflict("取样期间预演场景已变化，请重新读取后取样")
	}
	return samples, nil
}
