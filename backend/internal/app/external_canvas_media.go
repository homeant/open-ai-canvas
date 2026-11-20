package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

type ExternalCanvasReadRequest struct {
	CanvasID         string `json:"canvasId"`
	NodeID           string `json:"nodeId,omitempty"`
	Offset           int    `json:"offset,omitempty"`
	ConnectionOffset int    `json:"connectionOffset,omitempty"`
}
type ExternalCanvasMutation struct {
	CanvasID       string `json:"canvasId"`
	SnapshotHash   string `json:"snapshotHash"`
	IdempotencyKey string `json:"idempotencyKey"`
}
type ExternalCanvasMediaNodeRequest struct {
	ExternalCanvasMutation
	ResourceID string `json:"resourceId"`
	Title      string `json:"title"`
}
type ExternalCanvasConnectRequest struct {
	ExternalCanvasMutation
	FromNodeID string `json:"fromNodeId"`
	ToNodeID   string `json:"toNodeId"`
}
type ExternalCanvasVideoRequest struct {
	ExternalCanvasMutation
	NodeID           string   `json:"nodeId,omitempty"`
	Title            string   `json:"title"`
	Prompt           string   `json:"prompt"`
	LogicalModelID   string   `json:"logicalModelId,omitempty"`
	ChannelID        string   `json:"channelId,omitempty"`
	ChannelModelKey  string   `json:"channelModelKey,omitempty"`
	DurationSeconds  int      `json:"durationSeconds"`
	Size             string   `json:"size"`
	Resolution       string   `json:"resolution,omitempty"`
	GenerateAudio    *bool    `json:"generateAudio,omitempty"`
	ReferenceNodeIDs []string `json:"referenceNodeIds"`
	StartFrameNodeID string   `json:"startFrameNodeId,omitempty"`
	EndFrameNodeID   string   `json:"endFrameNodeId,omitempty"`
}
type ExternalCanvasGenerationRequest struct {
	CanvasID     string `json:"canvasId"`
	NodeID       string `json:"nodeId"`
	SnapshotHash string `json:"snapshotHash"`
}
type ExternalCanvasSubmitRequest struct {
	ExternalCanvasMutation
	NodeID              string `json:"nodeId"`
	ExpectedPreviewHash string `json:"expectedPreviewHash"`
	MaxCostMicrocredits int64  `json:"maxCostMicrocredits"`
}
type ExternalCanvasSyncRequest struct {
	ExternalCanvasMutation
	NodeID string `json:"nodeId"`
	TaskID string `json:"taskId"`
}

func (s *Service) externalCanvasDocument(userID, canvasID, snapshot string) (*model.CanvasProject, map[string]any, error) {
	canvas, err := s.repo.CanvasProjectForUser(userID, canvasID)
	if err != nil {
		return nil, nil, creationError(err)
	}
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		return nil, nil, err
	}
	if snapshot != "" && cloudAgentCanvasHash(doc) != snapshot {
		return nil, nil, creationConflict("画布快照已变化，请重新读取")
	}
	return canvas, doc, nil
}

// One receipt, canvas edit and billed task share the same transaction. Replay
// is checked before snapshot CAS so a lost response never queues a second task.
func (s *Service) externalCanvasMutation(userID, command string, scope ExternalCanvasMutation, input any, fn func(*Service, RuntimePolicySetting) (map[string]any, error)) (map[string]any, error) {
	if strings.TrimSpace(scope.IdempotencyKey) == "" || len(scope.IdempotencyKey) > 200 || len(scope.SnapshotHash) != 64 {
		return nil, BadAuthRequest("需要画布快照与1–200字节幂等键")
	}
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	var result map[string]any
	err := s.repo.Transaction(func(repo *repository.Repository) error {
		if err := repo.LockCanvasMutation(userID, scope.CanvasID); err != nil {
			return err
		}
		if _, err := repo.CanvasProjectForUser(userID, scope.CanvasID); err != nil {
			return creationError(err)
		}
		id := dramaContentRevision([]string{userID, scope.CanvasID, command, scope.IdempotencyKey})
		digest := dramaContentRevision(input)
		old, err := repo.CanvasMutationReceiptForUser(userID, scope.CanvasID, id)
		if err == nil {
			if old.PayloadDigest != digest {
				return creationConflict("幂等键已用于不同请求")
			}
			if old.ResultJSON == "" {
				return creationConflict("操作尚未完成")
			}
			return json.Unmarshal([]byte(old.ResultJSON), &result)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		local := &Service{repo: repo, dataDir: s.dataDir, pluginRuntime: s.pluginRuntime, pluginRuntimeErr: s.pluginRuntimeErr, coordinator: s.coordinator, pendingStorage: s.pendingStorage, routeCatalogTTL: s.routeCatalogTTL, routeCatalogMaxStale: s.routeCatalogMaxStale}
		if _, _, err := local.externalCanvasDocument(userID, scope.CanvasID, scope.SnapshotHash); err != nil {
			return err
		}
		policy, err := local.RuntimePolicy()
		if err != nil {
			return err
		}
		receipt := &model.CanvasMutationReceipt{ID: id, UserID: userID, CanvasID: scope.CanvasID, MutationID: id, PayloadDigest: digest}
		created, err := repo.TryCreateCanvasMutationReceipt(receipt)
		if err != nil {
			return err
		}
		if !created {
			return creationConflict("幂等操作竞争，请重试原请求")
		}
		result, err = fn(local, policy)
		if err != nil {
			return err
		}
		canvas, doc, err := local.externalCanvasDocument(userID, scope.CanvasID, "")
		if err != nil {
			return err
		}
		result["canvasId"], result["snapshotHash"], result["revision"] = scope.CanvasID, cloudAgentCanvasHash(doc), canvas.Revision
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		receipt.ResultJSON, receipt.Revision = string(encoded), canvas.Revision
		return repo.FinalizeCanvasMutationReceipt(receipt)
	})
	return result, creationError(err)
}

func (s *Service) externalCanvasNode(userID string, doc map[string]any, node map[string]any) (map[string]any, error) {
	meta, _ := node["metadata"].(map[string]any)
	result := map[string]any{"id": node["id"], "type": node["type"], "title": node["title"], "position": node["position"], "width": node["width"], "height": node["height"], "nodeHash": cloudAgentNodeHash(doc, stringValue(node["id"])), "qaStatus": "unreviewed"}
	for _, field := range []string{"status", "taskStatus", "taskId", "prompt", "composerContent", "model", "logicalModelId", "channelId", "channelModelKey", "size", "seconds", "vquality", "generateAudio", "watermark", "videoStartFrameNodeId", "videoEndFrameNodeId", "referenceNodeIds", "source", "reviewStatus"} {
		if value, ok := meta[field]; ok {
			result[field] = value
		}
	}
	// Only a validated canonical generation contract may leave the service;
	// arbitrary node metadata can contain custom-channel credentials.
	if stringValue(node["type"]) == "video" {
		selection, options, err := cloudAgentMediaDraftConfiguration(cloudAgentMediaArgs{Mode: "video", NodeID: stringValue(node["id"])}, doc)
		if err == nil {
			result["modelSelection"], result["options"] = selection, options
		}
	}
	if id := strings.TrimPrefix(stringValue(meta["storageKey"]), "resource:"); id != "" && strings.HasPrefix(stringValue(meta["storageKey"]), "resource:") {
		resource, err := s.repo.ResourceForUser(userID, id)
		if err != nil {
			return nil, creationError(err)
		}
		adoptions, err := s.repo.CurrentDramaMediaAdoptions(userID, id)
		if err != nil {
			return nil, err
		}
		result["resource"] = externalCanvasResource(resource)
		result["adopted"], result["adoptions"] = len(adoptions) > 0, adoptions
	} else {
		result["adopted"] = false
	}
	return result, nil
}
func externalCanvasResource(resource *model.Resource) map[string]any {
	return map[string]any{"id": resource.ID, "status": resource.Status, "kind": resource.Kind, "mimeType": resource.MimeType, "size": resource.Size, "width": resource.Width, "height": resource.Height, "durationMs": resource.DurationMs, "qaStatus": "unreviewed"}
}

// Reads never settle tasks or edit the canvas. Task completion is an explicit
// sync operation, so inspecting a formal production remains read-only.
func (s *Service) ExternalCanvasMedia(userID string, in ExternalCanvasReadRequest) (map[string]any, error) {
	if in.Offset < 0 || in.ConnectionOffset < 0 {
		return nil, BadAuthRequest("分页偏移不能为负")
	}
	canvas, doc, err := s.externalCanvasDocument(userID, in.CanvasID, "")
	if err != nil {
		return nil, err
	}
	nodes := creationMaps(doc["nodes"])
	resultNodes := []map[string]any{}
	if in.NodeID != "" {
		found := false
		for _, node := range nodes {
			if stringValue(node["id"]) == in.NodeID {
				value, err := s.externalCanvasNode(userID, doc, node)
				if err != nil {
					return nil, err
				}
				resultNodes = append(resultNodes, value)
				found = true
				break
			}
		}
		if !found {
			return nil, NotFound("画布节点不存在")
		}
	} else {
		start := min(in.Offset, len(nodes))
		end := start + min(100, len(nodes)-start)
		for i := start; i < end; i++ {
			value, err := s.externalCanvasNode(userID, doc, nodes[i])
			if err != nil {
				return nil, err
			}
			resultNodes = append(resultNodes, value)
		}
	}
	edges := []map[string]any{}
	for _, edge := range creationMaps(doc["connections"]) {
		if in.NodeID == "" || stringValue(edge["fromNodeId"]) == in.NodeID || stringValue(edge["toNodeId"]) == in.NodeID {
			edges = append(edges, map[string]any{"id": edge["id"], "fromNodeId": edge["fromNodeId"], "toNodeId": edge["toNodeId"], "fromHandleId": edge["fromHandleId"], "toHandleId": edge["toHandleId"]})
		}
	}
	start := min(in.ConnectionOffset, len(edges))
	end := start + min(500, len(edges)-start)
	nodeStart := min(in.Offset, len(nodes))
	return map[string]any{"canvasId": canvas.ID, "projectId": canvas.ProjectID, "title": canvas.Title, "revision": canvas.Revision, "snapshotHash": cloudAgentCanvasHash(doc), "nodes": resultNodes, "nodeCount": len(nodes), "nextOffset": nodeStart + min(100, len(nodes)-nodeStart), "connections": edges[start:end], "connectionCount": len(edges), "nextConnectionOffset": end, "readOnly": true}, nil
}

func (s *Service) AddExternalCanvasMediaNode(userID string, in ExternalCanvasMediaNodeRequest) (map[string]any, error) {
	return s.externalCanvasMutation(userID, "media.add", in.ExternalCanvasMutation, in, func(local *Service, policy RuntimePolicySetting) (map[string]any, error) {
		if strings.TrimSpace(in.Title) == "" || utf8.RuneCountInString(in.Title) > 240 {
			return nil, BadAuthRequest("需要不超过240字的审片节点标题")
		}
		resource, err := local.repo.ResourceForUser(userID, in.ResourceID)
		if err != nil {
			return nil, creationError(err)
		}
		if resource.Status != model.ResourceStatusReady || (resource.Kind != "image" && resource.Kind != "video" && resource.Kind != "audio") || !strings.HasPrefix(resource.MimeType, resource.Kind+"/") {
			return nil, BadAuthRequest("只能加入已就绪的自有图片、视频或音频")
		}
		canvas, doc, err := local.externalCanvasDocument(userID, in.CanvasID, in.SnapshotHash)
		if err != nil {
			return nil, err
		}
		id := "mcp-media-" + dramaContentRevision([]string{userID, in.CanvasID, in.IdempotencyKey})[:32]
		x, y := externalCanvasNextPosition(doc)
		meta := map[string]any{"storageKey": "resource:" + resource.ID, "content": resourceFileURL(resource.ID), "status": "success", "source": "mcp_upload_review", "reviewStatus": "unreviewed", "naturalWidth": resource.Width, "naturalHeight": resource.Height}
		node := creationAddedNode(CreationCanvasOp{Type: "add_node", ID: id, NodeType: resource.Kind, Title: in.Title, X: &x, Y: &y, Metadata: meta})
		if resource.Width > 0 && resource.Height > 0 {
			node["width"] = float64(360)
			node["height"] = float64(360) * float64(resource.Height) / float64(resource.Width)
		}
		doc["nodes"] = append(creationMaps(doc["nodes"]), node)
		if err := saveCloudAgentDocument(local.repo, canvas, doc, policy); err != nil {
			return nil, err
		}
		adoptions, err := local.repo.CurrentDramaMediaAdoptions(userID, resource.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"nodeId": id, "resourceId": resource.ID, "adopted": len(adoptions) > 0, "adoptions": adoptions, "qaStatus": "unreviewed", "committed": true}, nil
	})
}
func externalCanvasNextPosition(doc map[string]any) (float64, float64) {
	x := 80.0
	for _, node := range creationMaps(doc["nodes"]) {
		position, _ := node["position"].(map[string]any)
		left, _ := numericScalar(position["x"])
		width, _ := numericScalar(node["width"])
		x = math.Max(x, left+width+80)
	}
	return x, 80
}

func (s *Service) ConnectExternalCanvasNodes(userID string, in ExternalCanvasConnectRequest) (map[string]any, error) {
	return s.externalCanvasMutation(userID, "media.connect", in.ExternalCanvasMutation, in, func(local *Service, policy RuntimePolicySetting) (map[string]any, error) {
		_, doc, err := local.externalCanvasDocument(userID, in.CanvasID, in.SnapshotHash)
		if err != nil {
			return nil, err
		}
		nodes, _ := creationObjects(doc["nodes"])
		if stringValue(nodes[in.ToNodeID]["type"]) != "video" {
			return nil, BadAuthRequest("媒体连线目标须为视频草稿")
		}
		meta, _ := nodes[in.ToNodeID]["metadata"].(map[string]any)
		if reason, _ := cloudAgentMediaTargetIssue(nodes[in.ToNodeID], "video"); reason != "" || stringValue(meta["agentDraftRunId"]) != "" {
			return nil, BadAuthRequest("目标须为空闲视频草稿；Agent草稿请在原运行处理")
		}
		if _, _, err := cloudAgentReferenceDescriptor(nodes[in.FromNodeID]); err != nil {
			return nil, BadAuthRequest("来源须为真实媒体节点")
		}
		id := "mcp-edge-" + dramaContentRevision(in)[:32]
		call, err := externalPrevisCall("canvas_apply_ops", agentCanvasArgs{SnapshotHash: in.SnapshotHash, Ops: []agentCanvasOp{{Type: "connect_nodes", ID: id, FromNodeID: in.FromNodeID, ToNodeID: in.ToNodeID}}})
		if err != nil {
			return nil, err
		}
		_, err = applyCloudAgentCanvas(local.repo, userID, in.CanvasID, call, policy)
		return map[string]any{"edgeId": id, "committed": true}, err
	})
}

func (s *Service) ConfigureExternalCanvasVideo(userID string, in ExternalCanvasVideoRequest) (map[string]any, error) {
	return s.externalCanvasMutation(userID, "video.configure", in.ExternalCanvasMutation, in, func(local *Service, policy RuntimePolicySetting) (map[string]any, error) {
		id := in.NodeID
		if id == "" {
			id = "mcp-video-" + dramaContentRevision([]string{userID, in.CanvasID, in.IdempotencyKey})[:32]
		}
		a := cloudAgentMediaArgs{Mode: "video", ExternalCanvas: true, NodeID: id, Title: in.Title, Prompt: in.Prompt, SnapshotHash: in.SnapshotHash, LogicalModelID: in.LogicalModelID, ChannelID: in.ChannelID, ChannelModelKey: in.ChannelModelKey, Duration: in.DurationSeconds, Size: in.Size, Quality: in.Resolution, VideoGenerateAudio: in.GenerateAudio, ReferenceNodeIDs: in.ReferenceNodeIDs, VideoStartFrameNodeID: in.StartFrameNodeID, VideoEndFrameNodeID: in.EndFrameNodeID}
		req, plan, err := local.externalCanvasTaskRequest(userID, in.CanvasID, a)
		if err != nil {
			return nil, err
		}
		// Dry admission resolves the same model defaults as submission, with no
		// queued task, credit hold or Agent approval row.
		task, _, err := local.externalCanvasPrepare(userID, req, plan, math.MaxInt64)
		if err != nil {
			return nil, err
		}
		if err := createCloudAgentMediaNode(local.repo, userID, in.CanvasID, plan, nil, policy); err != nil {
			return nil, err
		}
		return map[string]any{"nodeId": id, "committed": true, "submitted": false, "resolvedModel": task.Model, "qaStatus": "unreviewed"}, nil
	})
}

func (s *Service) externalCanvasTaskRequest(userID, canvasID string, a cloudAgentMediaArgs) (CreateTaskRequest, *cloudAgentMediaPlan, error) {
	if (a.LogicalModelID != "" && (a.ChannelID != "" || a.ChannelModelKey != "")) || (a.LogicalModelID == "" && (a.ChannelID == "" || a.ChannelModelKey == "")) {
		return CreateTaskRequest{}, nil, BadAuthRequest("请选择一个逻辑模型或完整系统渠道模型")
	}
	if err := validateCloudAgentMediaShape(a, &cloudAgentRuntime{}); err != nil {
		return CreateTaskRequest{}, nil, err
	}
	_, _, refs, err := cloudAgentMediaDocument(s.repo, userID, canvasID, a)
	if err != nil {
		return CreateTaskRequest{}, nil, err
	}
	if err := validateCloudAgentMediaReferences("video", refs); err != nil {
		return CreateTaskRequest{}, nil, err
	}
	prompt, err := externalCanvasReferencePrompt(a.Prompt, refs)
	if err != nil {
		return CreateTaskRequest{}, nil, err
	}
	a.Prompt = prompt
	spec, err := cloudAgentGenerationSpec(a, refs, nil)
	if err != nil {
		return CreateTaskRequest{}, nil, err
	}
	config := spec.Options.TaskConfig()
	if a.ChannelID != "" {
		config["channelId"], config["channelModelKey"], config["model"] = a.ChannelID, a.ChannelModelKey, a.ChannelModelKey
	}
	operation, err := s.resolveCloudAgentMediaOperation(a, refs)
	if err != nil {
		return CreateTaskRequest{}, nil, err
	}
	metadata := map[string]any{"nodeId": a.NodeID, "source": "mcp_canvas_generation", "videoEditOperation": operation, "videoStartFrameNodeId": a.VideoStartFrameNodeID, "videoEndFrameNodeId": a.VideoEndFrameNodeID}
	refs["mode"], refs["prompt"], refs["config"], refs["metadata"] = "video", a.Prompt, config, metadata
	return CreateTaskRequest{ProjectID: canvasID, Type: "canvas_video", Operation: operation, Prompt: a.Prompt, LogicalModelID: a.LogicalModelID, Model: a.ChannelModelKey, Input: refs}, &cloudAgentMediaPlan{Args: a}, nil
}

var externalCanvasNodeMention = regexp.MustCompile(`@\[node:([^\]]+)\]`)

func externalCanvasReferencePrompt(prompt string, refs map[string]any) (string, error) {
	labels := map[string]string{}
	for _, kind := range []struct{ field, label string }{{"referenceImages", "图片"}, {"referenceVideos", "视频"}, {"referenceAudios", "音频"}} {
		for i, ref := range creationMaps(refs[kind.field]) {
			labels[stringValue(ref["id"])] = fmt.Sprintf("@%s%d", kind.label, i+1)
		}
	}
	missing := false
	prompt = externalCanvasNodeMention.ReplaceAllStringFunc(prompt, func(token string) string {
		match := externalCanvasNodeMention.FindStringSubmatch(token)
		label := labels[match[1]]
		if label == "" {
			missing = true
			return token
		}
		return label
	})
	if missing || strings.Contains(prompt, "@[node:") {
		return "", BadAuthRequest("提示词引用了没有真实输入连线的节点，请先连线并重新预检")
	}
	return cloudAgentMediaReferencePrompt(prompt, refs)
}

func (s *Service) externalCanvasExistingRequest(userID string, in ExternalCanvasGenerationRequest) (CreateTaskRequest, *cloudAgentMediaPlan, error) {
	_, doc, err := s.externalCanvasDocument(userID, in.CanvasID, in.SnapshotHash)
	if err != nil {
		return CreateTaskRequest{}, nil, err
	}
	nodes, err := creationObjects(doc["nodes"])
	if err != nil {
		return CreateTaskRequest{}, nil, err
	}
	node := nodes[in.NodeID]
	if node == nil || node["type"] != "video" {
		return CreateTaskRequest{}, nil, NotFound("视频节点不存在")
	}
	meta, _ := node["metadata"].(map[string]any)
	a := cloudAgentMediaArgs{Mode: "video", ExternalCanvas: true, NodeID: in.NodeID, SnapshotHash: in.SnapshotHash, Title: stringValue(node["title"]), Prompt: stringValue(meta["prompt"]), VideoStartFrameNodeID: stringValue(meta["videoStartFrameNodeId"]), VideoEndFrameNodeID: stringValue(meta["videoEndFrameNodeId"])}
	selection, options, err := cloudAgentMediaDraftConfiguration(a, doc)
	if err != nil {
		return CreateTaskRequest{}, nil, err
	}
	if selection == nil {
		return CreateTaskRequest{}, nil, BadAuthRequest("请先配置视频节点模型")
	}
	a.LogicalModelID, a.ChannelID, a.ChannelModelKey = selection.LogicalModelID, selection.ChannelID, selection.ModelKey
	applyCloudAgentMediaDraftOptions(&a, options)
	for _, edge := range creationMaps(doc["connections"]) {
		if stringValue(edge["toNodeId"]) == in.NodeID {
			id := stringValue(edge["fromNodeId"])
			if _, _, err := cloudAgentReferenceDescriptor(nodes[id]); err != nil {
				return CreateTaskRequest{}, nil, BadAuthRequest("当前 MCP 视频入口仅支持媒体输入连线，请在浏览器处理其他节点输入")
			}
			a.ReferenceNodeIDs = append(a.ReferenceNodeIDs, id)
		}
	}
	// References come exclusively from persisted incoming edges. Mentions never
	// manufacture a connection while a preview is being read.
	return s.externalCanvasTaskRequest(userID, in.CanvasID, a)
}

func (s *Service) externalCanvasPrepare(userID string, req CreateTaskRequest, plan *cloudAgentMediaPlan, ceiling int64) (*model.Task, *model.BillingOrder, error) {
	prepared := &creationTaskPreparation{}
	req.creationPrepare = prepared
	req.callerHoldsStorageMu = true
	req.admission = &taskAdmission{ID: newID(), MaxCharge: ceiling}
	task, err := s.CreateTask(userID, req)
	if err != nil {
		return nil, nil, err
	}
	if err = applyCloudAgentResolvedMediaDefaults(&req, plan, task); err != nil {
		return nil, nil, err
	}
	if prepared.Order != nil {
		accounts, err := s.repo.CreditAccounts([]string{userID})
		if err != nil {
			return nil, nil, err
		}
		if len(accounts) != 1 || accounts[0].AvailableMicrocredits < prepared.Order.AmountMicrocredits {
			return nil, nil, BadAuthRequest("积分不足，尚未提交")
		}
	}
	return task, prepared.Order, nil
}
func externalCanvasPreview(task *model.Task, order *model.BillingOrder) (map[string]any, error) {
	var input map[string]any
	if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		return nil, err
	}
	config, _ := input["config"].(map[string]any)
	options := map[string]any{}
	for _, key := range []string{"size", "videoSeconds", "vquality", "videoGenerateAudio", "videoWatermark"} {
		if value, ok := config[key]; ok {
			options[key] = value
		}
	}
	images := []map[string]any{}
	metadata, _ := input["metadata"].(map[string]any)
	for _, field := range []string{"referenceImages", "referenceVideos", "referenceAudios"} {
		for _, ref := range creationMaps(input[field]) {
			role := "reference"
			if ref["id"] == metadata["videoStartFrameNodeId"] {
				role = "first_frame"
			} else if ref["id"] == metadata["videoEndFrameNodeId"] {
				role = "last_frame"
			}
			images = append(images, map[string]any{"nodeId": ref["id"], "resourceId": strings.TrimPrefix(stringValue(ref["storageKey"]), "resource:"), "inputField": field, "inputOrder": len(images), "sent": true, "role": role})
		}
	}
	charge := int64(0)
	if order != nil {
		charge = order.AmountMicrocredits
	}
	hash := dramaContentRevision(map[string]any{"input": input, "operation": task.Operation, "model": task.Model, "logicalModelId": task.LogicalModelID, "routeId": task.RouteID, "amountMicrocredits": charge})
	mode := "unspecified"
	if hasNativeVideoAudio(model.ChannelInterfaceType(stringValue(config["interfaceType"])), task.Model) {
		mode = "native"
	} else if options["videoGenerateAudio"] != nil {
		mode = "switch"
	}
	return map[string]any{"previewHash": hash, "model": task.Model, "logicalModelId": task.LogicalModelID, "operation": task.Operation, "prompt": task.Prompt, "options": options, "orderedReferences": images, "amountMicrocredits": charge, "canSubmit": true, "submitted": false, "qaStatus": "unreviewed", "hasSpeech": nil, "audioQuality": "unverified", "audioOutputMode": mode}, nil
}
func (s *Service) PreviewExternalCanvasGeneration(userID string, in ExternalCanvasGenerationRequest) (map[string]any, error) {
	if len(in.SnapshotHash) != 64 {
		return nil, BadAuthRequest("需要当前画布 snapshotHash")
	}
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	req, plan, err := s.externalCanvasExistingRequest(userID, in)
	if err != nil {
		return nil, err
	}
	task, order, err := s.externalCanvasPrepare(userID, req, plan, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	result, err := externalCanvasPreview(task, order)
	if err != nil {
		return nil, err
	}
	result["canvasId"], result["nodeId"], result["snapshotHash"] = in.CanvasID, in.NodeID, in.SnapshotHash
	return result, nil
}
func (s *Service) SubmitExternalCanvasGeneration(userID string, in ExternalCanvasSubmitRequest) (map[string]any, error) {
	if s.IsDraining() {
		return nil, NewAppError(503, "服务维护中")
	}
	if in.MaxCostMicrocredits < 0 || len(in.ExpectedPreviewHash) != 64 {
		return nil, BadAuthRequest("需要预检指纹与非负费用上限")
	}
	result, err := s.externalCanvasMutation(userID, "video.submit", in.ExternalCanvasMutation, in, func(local *Service, policy RuntimePolicySetting) (map[string]any, error) {
		req, plan, err := local.externalCanvasExistingRequest(userID, ExternalCanvasGenerationRequest{CanvasID: in.CanvasID, NodeID: in.NodeID, SnapshotHash: in.SnapshotHash})
		if err != nil {
			return nil, err
		}
		task, order, err := local.externalCanvasPrepare(userID, req, plan, in.MaxCostMicrocredits)
		if err != nil {
			return nil, err
		}
		preview, err := externalCanvasPreview(task, order)
		if err != nil {
			return nil, err
		}
		if preview["previewHash"] != in.ExpectedPreviewHash {
			return nil, creationConflict("模型报价或实际输入已变化，请重新预检")
		}
		var input map[string]any
		if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
			return nil, err
		}
		if err := local.protectTaskSecrets(input); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		task.InputJSON = string(encoded)
		if order != nil {
			task.BillingOrderID = order.ID
		}
		if err := createCloudAgentMediaNode(local.repo, userID, in.CanvasID, plan, task, policy); err != nil {
			return nil, err
		}
		if err := createTaskWithStorageQuotaRepository(local.repo, task, order, policy); err != nil {
			return nil, err
		}
		return map[string]any{"taskId": task.ID, "nodeId": in.NodeID, "status": task.Status, "submitted": true, "previewHash": in.ExpectedPreviewHash, "adopted": false, "qaStatus": "unreviewed"}, nil
	})
	if err == nil {
		s.wakeTaskDispatcher()
	}
	return result, err
}

func (s *Service) GetExternalCanvasGeneration(userID, canvasID, nodeID string) (map[string]any, error) {
	_, doc, err := s.externalCanvasDocument(userID, canvasID, "")
	if err != nil {
		return nil, err
	}
	nodes, _ := creationObjects(doc["nodes"])
	node := nodes[nodeID]
	if node == nil {
		return nil, NotFound("节点不存在")
	}
	meta, _ := node["metadata"].(map[string]any)
	task, err := s.repo.TaskForUser(userID, stringValue(meta["taskId"]))
	if err != nil {
		return nil, creationError(err)
	}
	if task.ProjectID != canvasID {
		return nil, Forbidden("任务不属于该画布")
	}
	result := map[string]any{"canvasId": canvasID, "nodeId": nodeID, "taskId": task.ID, "status": task.Status, "progress": task.Progress, "snapshotHash": cloudAgentCanvasHash(doc), "qaStatus": "unreviewed", "adopted": false, "readOnly": true}
	if task.Status == model.TaskStatusSucceeded {
		id, _ := taskOutputResource(task.ResultJSON, task.Type)
		resource, err := s.repo.ResourceForUser(userID, id)
		if err != nil {
			return nil, creationError(err)
		}
		result["resource"] = externalCanvasResource(resource)
		adoptions, err := s.repo.CurrentDramaMediaAdoptions(userID, id)
		if err != nil {
			return nil, err
		}
		result["adopted"], result["adoptions"] = len(adoptions) > 0, adoptions
	}
	return result, nil
}
func (s *Service) SyncExternalCanvasGeneration(userID string, in ExternalCanvasSyncRequest) (map[string]any, error) {
	return s.externalCanvasMutation(userID, "video.sync", in.ExternalCanvasMutation, in, func(local *Service, policy RuntimePolicySetting) (map[string]any, error) {
		task, err := local.repo.TaskForUser(userID, in.TaskID)
		if err != nil {
			return nil, creationError(err)
		}
		if task.ProjectID != in.CanvasID {
			return nil, Forbidden("任务不属于该画布")
		}
		var input struct {
			Metadata struct{ NodeID, Source string }
		}
		if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
			return nil, err
		}
		if input.Metadata.Source != "mcp_canvas_generation" || input.Metadata.NodeID != in.NodeID {
			return nil, BadAuthRequest("只能同步本入口创建且绑定到该节点的任务")
		}
		if !cloudAgentTaskTerminal(task.Status) {
			return nil, BadAuthRequest("任务尚未结束，请稍后查询")
		}
		id, err := completeCloudAgentMediaNode(local.repo, userID, in.CanvasID, in.NodeID, task, policy)
		if err != nil {
			return nil, err
		}
		return map[string]any{"nodeId": id, "taskId": task.ID, "status": task.Status, "committed": true, "adopted": false, "qaStatus": "unreviewed"}, nil
	})
}
