package handler

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"yingce/backend/internal/service"
)

func registerDramaShotMediaMCP(server *mcp.Server, svc *service.Service, userID string) {
	externalMCPTool(server, "list_canvas_image_candidates", "只读列出当前作品画布中已就绪图片的实际 resourceId/nodeId/nodeHash；节点存在不代表已送入分镜生成请求", func(_ context.Context, in struct {
		DramaID   string `json:"dramaId"`
		EpisodeID string `json:"episodeId"`
		CanvasID  string `json:"canvasId"`
	}) (any, error) {
		return svc.DramaCanvasImageCandidates(userID, in.DramaID, in.EpisodeID, in.CanvasID)
	})
	externalMCPTool(server, "bind_shot_reference_images", "按稳定 shotId 绑定实体信息图或段首首帧，[]清除；需整集内容指纹。dryRun=true 只读计算实际模型输入顺序/资源/送模状态，不保存回执或绑定；false 需幂等键并使提示词待复核。首帧仅支持段首镜头；内部镜头用 information 或独立候选版本。节点来源需 canvasId/nodeId/nodeHash；也可直接引用自有已就绪 resourceId", func(_ context.Context, in service.DramaShotImagesRequest) (any, error) {
		return svc.BindDramaShotImages(userID, in)
	})
	externalMCPTool(server, "list_shot_media_versions", "读取镜头内容指纹、明确图片绑定、独立候选版本和所在段历史视频的镜头窗及视觉 QA；旧段版本分镜不明保持 unknown，不把 ready 或当前版本当验收通过", func(_ context.Context, in service.DramaShotMediaScope) (any, error) {
		return svc.DramaShotMediaVersions(userID, in)
	})
	externalMCPTool(server, "register_shot_media_version", "登记自有图片/视频为镜头候选版本，明确源裁切起点与时长；需镜头指纹及幂等键。不采用、不裁剪文件、不改正式片；静图仅能验收静态信息，不证明动态连续性，时长变化需独立重对齐", func(_ context.Context, in service.RegisterDramaShotMediaRequest) (any, error) {
		return svc.RegisterDramaShotMedia(userID, in)
	})
	externalMCPTool(server, "review_shot_media_version", "对明确镜头版本追加 picture/text/continuity 的 approved/rejected/unknown 人工观察与退回原因；需完整实际裁切窗、镜头指纹、expectedReviewId 和幂等键。静图不可验收动态连续性；旧分镜不明的版本可以退回但不能通过。不会替换或生成正式视频", func(_ context.Context, in service.DramaShotMediaReviewRequest) (any, error) {
		return svc.ReviewDramaShotMedia(userID, in)
	})
}
