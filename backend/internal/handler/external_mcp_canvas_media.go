package handler

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"yingce/backend/internal/service"
)

func registerCanvasMediaMCP(server *mcp.Server, svc *service.Service, userID string) {
	for _, name := range []string{"list_canvas_media_nodes", "get_canvas_media_node"} {
		externalMCPTool(server, name, "只读当前账号画布真实节点、连线、生成配置、资源及正式采用状态；节点每页100，连线每页500。get需nodeId；不输出自定义渠道密钥或媒体URL，不自动同步任务或验收", func(_ context.Context, in service.ExternalCanvasReadRequest) (any, error) {
			if name == "get_canvas_media_node" && in.NodeID == "" {
				return nil, service.BadAuthRequest("需要nodeId")
			}
			return svc.ExternalCanvasMedia(userID, in)
		})
	}
	externalMCPTool(server, "add_canvas_media_node", "把已上传的自有ready资源加入独立审片节点；需要当前画布snapshotHash与幂等键。不会采用到设定、分镜、段或成片，也不代表视觉／声音验收通过", func(_ context.Context, in service.ExternalCanvasMediaNodeRequest) (any, error) {
		return svc.AddExternalCanvasMediaNode(userID, in)
	})
	externalMCPTool(server, "connect_canvas_nodes", "建立真实媒体节点到空闲视频草稿的输入连线，复用画布连接校验；需要snapshotHash和幂等键，不能仅在提示词写节点ID假装已连线", func(_ context.Context, in service.ExternalCanvasConnectRequest) (any, error) {
		return svc.ConnectExternalCanvasNodes(userID, in)
	})
	for _, name := range []string{"create_canvas_video_node", "configure_canvas_video_node"} {
		externalMCPTool(server, name, "创建或配置真实空闲视频草稿及按顺序排列的输入连线；需模型、prompt、size（如9:16）、durationSeconds（如4）、snapshotHash与幂等键。resolution取list_models支持值。nodeId省略则创建；已有产物/运行中的节点拒绝覆盖。只预检配置不排队、不收费", func(_ context.Context, in service.ExternalCanvasVideoRequest) (any, error) {
			if name == "create_canvas_video_node" && in.NodeID != "" {
				return nil, service.BadAuthRequest("创建工具请省略nodeId；已有草稿用configure_canvas_video_node")
			}
			if name == "configure_canvas_video_node" && in.NodeID == "" {
				return nil, service.BadAuthRequest("配置工具需要nodeId")
			}
			return svc.ConfigureExternalCanvasVideo(userID, in)
		})
	}
	externalMCPTool(server, "preview_canvas_generation", "只读按视频节点已保存模型、规格、实际输入连线解析请求并走与浏览器相同的任务准入；返回真实资源/顺序、费用和previewHash，不提交、不扣费、不验收。需要当前snapshotHash", func(_ context.Context, in service.ExternalCanvasGenerationRequest) (any, error) {
		return svc.PreviewExternalCanvasGeneration(userID, in)
	})
	externalMCPTool(server, "submit_canvas_generation", "明确提交画布视频生成，会冻结报价积分；需snapshotHash、expectedPreviewHash、maxCostMicrocredits与幂等键。节点/账单/任务原子保存，重试不重复收费。不走通用create_video工作台，不自动采用正式作品", func(ctx context.Context, in service.ExternalCanvasSubmitRequest) (any, error) {
		policy, err := svc.RuntimePolicy()
		if err != nil {
			return nil, err
		}
		if err := externalMCPAllow(ctx, svc, "tasks:"+userID, policy.Request.TaskCreatePerMinute); err != nil {
			return nil, err
		}
		return svc.SubmitExternalCanvasGeneration(userID, in)
	})
	externalMCPTool(server, "get_canvas_generation", "只读节点绑定的实际生成任务和成功产物资源；不会因AAC、任务成功或ready而通过对白/视觉QA。画布结果回写另用sync_canvas_generation", func(_ context.Context, in struct {
		CanvasID string `json:"canvasId"`
		NodeID   string `json:"nodeId"`
	}) (any, error) {
		return svc.GetExternalCanvasGeneration(userID, in.CanvasID, in.NodeID)
	})
	externalMCPTool(server, "sync_canvas_generation", "将本入口已结束任务回写至仍绑定同taskId的原视频节点；需snapshotHash、nodeId、taskId与幂等键。节点删除/换任务时拒绝，不重建或采用正式视频", func(_ context.Context, in service.ExternalCanvasSyncRequest) (any, error) {
		return svc.SyncExternalCanvasGeneration(userID, in)
	})
}
