package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"yingce/backend/internal/model"
	"yingce/backend/internal/service"
)

type mcpRequestUserKey struct{}

func externalMCPTool[In any](server *mcp.Server, name, description string, fn func(context.Context, In) (any, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, _ *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		value, err := fn(ctx, input)
		var appErr *service.AppError
		if errors.As(err, &appErr) {
			body := map[string]any{"code": appErr.Code, "reason": appErr.Reason, "msg": appErr.Message, "details": appErr.Details, "retryable": appErr.Retryable}
			data, _ := json.Marshal(body)
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
		}
		if result, ok := value.(*mcp.CallToolResult); ok && err == nil {
			return result, nil, nil
		}
		return nil, value, err
	})
}

func externalMCPAllow(ctx context.Context, svc *service.Service, subject string, limit int) error {
	allowed, err := svc.AllowRequest(ctx, subject, limit, time.Minute)
	if err != nil {
		return err
	}
	if !allowed {
		retry := svc.RequestRetryAfter(ctx, subject, time.Minute)
		if retry < time.Millisecond {
			retry = time.Millisecond
		}
		err := service.RateLimited("请求过于频繁，请按 retryAfterMs 等待后重试")
		err.Details = map[string]any{"retryAfterMs": (retry + time.Millisecond - 1) / time.Millisecond, "retryAfterSeconds": (retry + time.Second - 1) / time.Second, "retryAt": time.Now().Add(retry).UTC().Format(time.RFC3339Nano)}
		return err
	}
	return nil
}

func externalMCPServer(svc *service.Service, user *model.User) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "yingce-video-creation", Version: "1.0.0"}, nil)
	userID := user.ID
	registerDramaEditingMCP(server, svc, userID)
	externalMCPTool(server, "list_models", "列出当前用户可用的创作模型及能力", func(_ context.Context, _ struct{}) (any, error) {
		return svc.ModelCatalogForUser(user, nil)
	})
	externalMCPTool(server, "quote_model", "按创作意图报价，生成前先调用", func(_ context.Context, input struct {
		ModelID string                     `json:"modelId" jsonschema:"模型 ID"`
		Intent  service.ModelRequestIntent `json:"intent"`
	}) (any, error) {
		return svc.QuoteLogicalModel(userID, input.ModelID, input.Intent)
	})
	externalMCPTool(server, "list_resources", "列出当前用户已上传的素材资源", func(_ context.Context, input struct {
		Limit int `json:"limit"`
	}) (any, error) {
		limit := input.Limit
		if limit <= 0 || limit > 100 {
			limit = 50
		}
		return svc.Resources(userID, limit)
	})
	externalMCPTool(server, "import_resource", "从 HTTPS 地址导入图片、音频或视频，返回资源 ID；服务端检查地址与配额", func(ctx context.Context, input struct {
		URL            string `json:"url"`
		Kind           string `json:"kind"`
		IdempotencyKey string `json:"idempotencyKey"`
	}) (any, error) {
		if input.IdempotencyKey == "" {
			return nil, service.BadAuthRequest("导入素材需要幂等键")
		}
		policy, err := svc.RuntimePolicy()
		if err != nil {
			return nil, err
		}
		if err := externalMCPAllow(ctx, svc, "resources-import:"+userID, policy.Request.ResourceImportPerMinute); err != nil {
			return nil, err
		}
		return svc.ImportResourceURL(userID, input.URL, input.Kind, 0, 0, 0, input.IdempotencyKey)
	})
	externalMCPTool(server, "get_resource_access", "签发当前用户素材的临时下载或模型输入地址", func(_ context.Context, input struct {
		Items []service.ResourceAccessRequest `json:"items"`
	}) (any, error) {
		return svc.ResourceAccessBatch(userID, input.Items)
	})
	externalMCPTool(server, "create_video", "创建异步视频任务；支持文生视频、首尾帧、参考图、参考视频和音频。必须提供幂等键", func(ctx context.Context, input service.WorkbenchTaskInput) (any, error) {
		if input.Kind != "video" || strings.TrimSpace(input.IdempotencyKey) == "" {
			return nil, service.BadAuthRequest("视频任务需要 kind=video 和 idempotencyKey")
		}
		policy, err := svc.RuntimePolicy()
		if err != nil {
			return nil, err
		}
		if err := externalMCPAllow(ctx, svc, "tasks:"+userID, policy.Request.TaskCreatePerMinute); err != nil {
			return nil, err
		}
		return svc.CreateWorkbenchTask(userID, input, "", "")
	})
	externalMCPTool(server, "get_task", "查询生成任务状态、结果资源 ID 和账单", func(_ context.Context, input struct {
		TaskID string `json:"taskId"`
	}) (any, error) {
		return svc.Task(userID, input.TaskID)
	})
	externalMCPTool(server, "cancel_task", "取消当前用户的生成任务", func(ctx context.Context, input struct {
		TaskID string `json:"taskId"`
	}) (any, error) {
		return svc.CancelTask(ctx, userID, input.TaskID)
	})
	externalMCPTool(server, "drama_models", "列出短剧自动制作可用的模型与默认配置", func(_ context.Context, _ struct{}) (any, error) {
		return svc.DramaModelCatalog(userID)
	})
	externalMCPTool(server, "estimate_drama", "估算短剧作品的生成任务与费用", func(_ context.Context, input service.DramaEstimateRequest) (any, error) {
		return svc.EstimateDrama(userID, input)
	})
	externalMCPTool(server, "create_drama", "从小说、剧本或分镜章节创建完整短剧作品；可先保存草稿，确认预算后启动。必须提供幂等键", func(_ context.Context, input struct {
		IdempotencyKey string                     `json:"idempotencyKey"`
		Drama          service.CreateDramaRequest `json:"drama"`
	}) (any, error) {
		return svc.CreateExternalDrama(userID, input.IdempotencyKey, input.Drama)
	})
	externalMCPTool(server, "list_dramas", "列出当前用户的短剧作品", func(_ context.Context, input struct {
		Page     int `json:"page"`
		PageSize int `json:"pageSize"`
	}) (any, error) {
		if input.Page <= 0 {
			input.Page = 1
		}
		if input.PageSize <= 0 || input.PageSize > 100 {
			input.PageSize = 24
		}
		return svc.ListDramas(userID, service.DramaListRequest{Page: input.Page, PageSize: input.PageSize})
	})
	externalMCPTool(server, "get_drama", "读取短剧作品和制作状态", func(_ context.Context, input struct {
		DramaID string `json:"dramaId"`
	}) (any, error) {
		return svc.DramaSummary(userID, input.DramaID)
	})
	externalMCPTool(server, "start_drama", "启动短剧自动制作；会按作品设置发起计费生成", func(_ context.Context, input struct {
		DramaID string `json:"dramaId"`
	}) (any, error) {
		return svc.SetDramaRunning(userID, input.DramaID, true)
	})
	externalMCPTool(server, "pause_drama", "暂停短剧作品后续调度", func(_ context.Context, input struct {
		DramaID string `json:"dramaId"`
	}) (any, error) {
		return svc.SetDramaRunning(userID, input.DramaID, false)
	})
	externalMCPTool(server, "get_drama_jobs", "查询短剧制作队列和失败原因", func(_ context.Context, input struct {
		DramaID string `json:"dramaId"`
	}) (any, error) {
		return svc.DramaJobs(userID, input.DramaID, 60)
	})
	externalMCPTool(server, "get_episode", "读取剧集、剧情段和已生成视频", func(_ context.Context, input struct {
		DramaID   string `json:"dramaId"`
		EpisodeID string `json:"episodeId"`
	}) (any, error) {
		return svc.DramaEpisodeDetail(userID, input.DramaID, input.EpisodeID)
	})
	externalMCPTool(server, "get_segment", "定向读取一个剧情段的生效提示词、镜头、参考图、生成请求预演、视频状态及人工提示词历史", func(_ context.Context, input struct {
		DramaID      string `json:"dramaId"`
		EpisodeID    string `json:"episodeId"`
		SegmentIndex int    `json:"segmentIndex"`
	}) (any, error) {
		return svc.DramaSegmentDetail(userID, input.DramaID, input.EpisodeID, input.SegmentIndex)
	})
	externalMCPTool(server, "list_segment_versions", "列出本段视频版本、当前版本及各版本资源和尺寸时长", func(_ context.Context, input struct {
		DramaID      string `json:"dramaId"`
		EpisodeID    string `json:"episodeId"`
		SegmentIndex int    `json:"segmentIndex"`
	}) (any, error) {
		return svc.DramaSegmentVideoVersions(userID, input.DramaID, input.EpisodeID, input.SegmentIndex)
	})
	externalMCPTool(server, "set_segment_current_version", "将本段已有视频版本设为当前版本；可用 expectedRevision 防止覆盖新版本", func(_ context.Context, input struct {
		DramaID          string `json:"dramaId"`
		EpisodeID        string `json:"episodeId"`
		SegmentIndex     int    `json:"segmentIndex"`
		TakeID           string `json:"takeId"`
		ExpectedRevision *int64 `json:"expectedRevision,omitempty"`
	}) (any, error) {
		return svc.RestoreDramaSegmentTake(userID, input.DramaID, input.EpisodeID, input.SegmentIndex, service.RestoreDramaSegmentTakeRequest{TakeID: input.TakeID, ExpectedRevision: input.ExpectedRevision})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "preview_segment_frame", Description: "提取本段当前或指定历史视频在某秒的预览帧，同时返回实测尺寸、时长和有无音轨"}, func(ctx context.Context, _ *mcp.CallToolRequest, input struct {
		DramaID      string  `json:"dramaId"`
		EpisodeID    string  `json:"episodeId"`
		SegmentIndex int     `json:"segmentIndex"`
		TakeID       string  `json:"takeId,omitempty"`
		AtSeconds    float64 `json:"atSeconds"`
	}) (*mcp.CallToolResult, any, error) {
		frame, err := svc.DramaSegmentPreviewFrame(ctx, userID, input.DramaID, input.EpisodeID, input.SegmentIndex, input.TakeID, input.AtSeconds)
		if err != nil {
			return nil, nil, err
		}
		metadata, _ := json.Marshal(frame)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(metadata)}, &mcp.ImageContent{MIMEType: "image/jpeg", Data: frame.JPEG}}}, nil, nil
	})
	externalMCPTool(server, "list_previs_capabilities", "读取预演台可用的模板、语义操作、坐标系与批量限制", func(_ context.Context, _ struct{}) (any, error) {
		return svc.ExternalPrevisCapabilities(), nil
	})
	externalMCPTool(server, "list_previs_scenes", "按 canvasId 或作品和剧集列出预演场景、节点与修订快照", func(_ context.Context, input service.ExternalPrevisScope) (any, error) {
		return svc.ListExternalPrevisScenes(userID, input)
	})
	externalMCPTool(server, "create_previs_canvas", "为作品和剧集创建预演画布；需要幂等键", func(ctx context.Context, input service.ExternalPrevisCanvasCreateRequest) (any, error) {
		if err := externalMCPAllow(ctx, svc, "previs-write:"+userID, 30); err != nil {
			return nil, err
		}
		return svc.CreateExternalPrevisCanvas(userID, input)
	})
	externalMCPTool(server, "list_previs_references", "列出剧集镜头及作品角色、场景和道具设定，返回可关联 ID", func(_ context.Context, input service.ExternalPrevisScope) (any, error) {
		return svc.ListExternalPrevisReferences(userID, input)
	})
	externalMCPTool(server, "upload_previs_model", "上传最多 2 MiB 的 GLB 2.0 模型，返回资源 ID 和可选子节点索引；大文件可用 import_resource 导入 HTTPS URL", func(ctx context.Context, input service.ExternalPrevisModelUploadRequest) (any, error) {
		if err := externalMCPAllow(ctx, svc, "resources-import:"+userID, 20); err != nil {
			return nil, err
		}
		return svc.UploadExternalPrevisModel(userID, input)
	})
	externalMCPTool(server, "inspect_previs_model", "检查已有 GLB 资源并列出可独立放置的 mesh 节点索引", func(_ context.Context, input struct {
		ResourceID string `json:"resourceId"`
	}) (any, error) {
		return svc.ExternalPrevisModelNodes(userID, input.ResourceID)
	})
	externalMCPTool(server, "upload_previs_render", "上传最多 2 MiB 的 PNG 单帧或 WebM 预览；更大的文件可由 import_resource 导入 HTTPS URL", func(ctx context.Context, input service.ExternalPrevisRenderUploadRequest) (any, error) {
		if err := externalMCPAllow(ctx, svc, "resources-import:"+userID, 20); err != nil {
			return nil, err
		}
		return svc.UploadExternalPrevisRender(userID, input)
	})
	externalMCPTool(server, "get_previs_scene", "按 canvasId 与 sceneId/nodeId 读取场景及 revision/snapshotHash；shotId 返回独立关键帧和首末状态，includeTransforms 返回米制坐标", func(_ context.Context, input service.ExternalPrevisReadRequest) (any, error) {
		return svc.GetExternalPrevisScene(userID, input)
	})
	externalMCPTool(server, "create_previs_scene", "按画布快照创建独立预演场景和工作站节点；需要幂等键，dryRun 可先看差异", func(ctx context.Context, input service.ExternalPrevisCreateRequest) (any, error) {
		if err := externalMCPAllow(ctx, svc, "previs-write:"+userID, 30); err != nil {
			return nil, err
		}
		return svc.CreateExternalPrevisScene(userID, input)
	})
	externalMCPTool(server, "apply_previs_patch", "按场景 snapshotHash 批量应用最多 32 项语义补丁；需要幂等键，dryRun 可先看差异", func(ctx context.Context, input service.ExternalPrevisPatchRequest) (any, error) {
		if err := externalMCPAllow(ctx, svc, "previs-write:"+userID, 30); err != nil {
			return nil, err
		}
		return svc.ApplyExternalPrevisPatch(userID, input)
	})
	externalMCPTool(server, "preview_previs_shot", "只读检查白模预演参数；实际生成请调用 render_previs_frame 或 render_previs_video", func(_ context.Context, input service.ExternalPrevisPreviewRequest) (any, error) {
		return svc.ExternalPrevisPreviewPlan(userID, input)
	})
	externalMCPTool(server, "sample_previs_frames", "只读取样同一镜头的 1–8 个时间点，返回真实 PNG 与逐帧包围盒重叠风险；不保存预览、不修改场景，不是 IK 或精确碰撞验收", func(ctx context.Context, input service.ExternalPrevisSampleRequest) (any, error) {
		if err := externalMCPAllow(ctx, svc, "previs-render:"+userID, 4); err != nil {
			return nil, err
		}
		samples, err := svc.SampleExternalPrevisFrames(ctx, userID, input)
		if err != nil {
			return nil, err
		}
		metadata, err := json.Marshal(samples)
		if err != nil {
			return nil, err
		}
		content := []mcp.Content{&mcp.TextContent{Text: string(metadata)}}
		for _, frame := range samples.Frames {
			label, _ := json.Marshal(map[string]any{"time": frame.Time, "diagnostics": frame.Diagnostics})
			content = append(content, &mcp.TextContent{Text: string(label)}, &mcp.ImageContent{MIMEType: frame.MimeType, Data: frame.PNG})
		}
		return &mcp.CallToolResult{Content: content}, nil
	})
	externalMCPTool(server, "render_previs_frame", "在服务端用真实预演视口渲染镜头白模 PNG，保存并绑定资源；需要场景快照和幂等键", func(ctx context.Context, input service.ExternalPrevisRenderRequest) (any, error) {
		if err := externalMCPAllow(ctx, svc, "previs-render:"+userID, 4); err != nil {
			return nil, err
		}
		input.Kind = "frame"
		result, err := svc.RenderExternalPrevis(ctx, userID, input)
		if err != nil {
			return nil, err
		}
		resource, body, err := svc.OpenResource(userID, result["resourceId"].(string))
		if err != nil {
			return nil, err
		}
		defer body.Close()
		data, err := io.ReadAll(io.LimitReader(body, (8<<20)+1))
		if err != nil {
			return nil, err
		}
		if len(data) > 8<<20 {
			return result, nil
		}
		metadata, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(metadata)}, &mcp.ImageContent{MIMEType: resource.MimeType, Data: data}}}, nil
	})
	externalMCPTool(server, "render_previs_video", "在服务端用真实预演视口录制最长 10 秒白模 WebM，保存并绑定资源；需要场景快照和幂等键", func(ctx context.Context, input service.ExternalPrevisRenderRequest) (any, error) {
		if err := externalMCPAllow(ctx, svc, "previs-render:"+userID, 4); err != nil {
			return nil, err
		}
		input.Kind = "video"
		return svc.RenderExternalPrevis(ctx, userID, input)
	})
	externalMCPTool(server, "validate_previs_scene", "检查镜头关联、竖屏画幅和模型资源是否齐全", func(_ context.Context, input service.ExternalPrevisReadRequest) (any, error) {
		return svc.ValidateExternalPrevisScene(userID, input)
	})
	externalMCPTool(server, "publish_previs_render", "把已上传的 PNG/WebM 预览资源关联到镜头；需要场景快照和幂等键", func(ctx context.Context, input service.ExternalPrevisRenderPublishRequest) (any, error) {
		if err := externalMCPAllow(ctx, svc, "previs-write:"+userID, 30); err != nil {
			return nil, err
		}
		return svc.PublishExternalPrevisRender(userID, input)
	})
	externalMCPTool(server, "get_previs_render", "读取镜头预览资源 ID 和媒体类型；使用 get_resource_access 获取临时地址", func(_ context.Context, input service.ExternalPrevisReadRequest) (any, error) {
		return svc.GetExternalPrevisRender(userID, input)
	})
	externalMCPTool(server, "approve_episode", "批准剧集继续制作；仅在用户明确要求时调用", func(_ context.Context, input struct {
		DramaID   string `json:"dramaId"`
		EpisodeID string `json:"episodeId"`
	}) (any, error) {
		return svc.ApproveDramaEpisode(userID, input.DramaID, input.EpisodeID)
	})
	externalMCPTool(server, "generate_segments", "按段生成或重生成视频；需要幂等键；单段用 request.expectedPromptRevision，多段用 expectedSegmentRevisions 逐段校验，原子排队且重试不重复生成", func(_ context.Context, input service.GenerateDramaMCPRequest) (any, error) {
		return svc.GenerateDramaSegmentsMCP(userID, input)
	})
	externalMCPTool(server, "compose_episode", "合成当前剧集已有的剧情段视频；需要 expectedRevision 和幂等键；返回紧凑 jobId/taskId/revision/资源/状态回执，详情另读 get_episode。不会批准剧集", func(_ context.Context, input service.ComposeDramaMCPRequest) (any, error) {
		return svc.ComposeDramaEpisodeMCP(userID, input)
	})
	return server
}

func RegisterExternalMCPRoutes(api *gin.RouterGroup, svc *service.Service) {
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		user, _ := r.Context().Value(mcpRequestUserKey{}).(*model.User)
		if user == nil {
			return nil
		}
		return externalMCPServer(svc, user)
	}, &mcp.StreamableHTTPOptions{Stateless: true, MaxRequestBodyBytes: 4 << 20})
	serve := func(c *gin.Context) {
		origin := mcpPublicOrigin(c)
		if origin == "" {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		if requestOrigin := c.GetHeader("Origin"); requestOrigin != "" && requestOrigin != origin {
			c.Status(http.StatusForbidden)
			return
		}
		challenge := `Bearer resource_metadata="` + origin + `/.well-known/oauth-protected-resource/api/mcp"`
		token := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if !strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") || token == "" {
			c.Header("WWW-Authenticate", challenge)
			c.Status(http.StatusUnauthorized)
			return
		}
		user, _, err := svc.UserForAPIKey(token)
		if err != nil {
			c.Header("WWW-Authenticate", challenge)
			c.Status(http.StatusUnauthorized)
			return
		}
		if !enforceRateLimit(c, "external-mcp:"+user.ID, 120, time.Minute) {
			return
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), mcpRequestUserKey{}, user))
		c.Header("Cache-Control", "no-store")
		handler.ServeHTTP(c.Writer, c.Request)
	}
	api.GET("/mcp", serve)
	api.POST("/mcp", serve)
}
