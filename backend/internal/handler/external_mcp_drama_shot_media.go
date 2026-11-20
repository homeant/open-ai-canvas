package handler

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"yingce/backend/internal/service"
)

func registerDramaShotMediaMCP(server *mcp.Server, svc *service.Service, userID string) {
	externalMCPTool(server, "list_canvas_image_candidates", "只读列出作品画布的真实图片resourceId/nodeId/nodeHash。offset按筛选后候选计数，limit默认100且最多100；返回total/nextOffset/hasMore/snapshotHash。可按resourceId精确查询后上传图片；翻页传回snapshotHash防止漏项。节点存在不代表已送模", func(_ context.Context, in service.DramaCanvasImageCandidatesRequest) (any, error) {
		return svc.DramaCanvasImageCandidates(userID, in)
	})
	externalMCPTool(server, "bind_shot_reference_images", "按shotId绑定实体信息图/段首首帧，[]清除，需整集内容指纹。dryRun=true只读，false需幂等键并使提示词待复核；两者均返回referenceChanges中的旧新编号mapping、promptConflicts与warnings，不能把旧人工提示词编号直接用于新输入。不会自动改正文或生成。首帧仅支持段首；可指定自有ready resourceId或canvasId/nodeId/nodeHash", func(_ context.Context, in service.DramaShotImagesRequest) (any, error) {
		return svc.BindDramaShotImages(userID, in)
	})
	externalMCPTool(server, "list_shot_media_versions", "读取镜头指纹、图片绑定、候选和段视频镜头窗及QA。顶层shotRevision/reviewExpectedShotRevision是审核用的当前单镜指纹；段视频take.shotRevision是整段指纹，二者不同仍可scriptMatch=matched。takeShotRevisionScope标明范围，qa.latestReviewIds按layer返回最新审核ID；旧分镜不明保持unknown，ready不算验收", func(_ context.Context, in service.DramaShotMediaScope) (any, error) {
		return svc.DramaShotMediaVersions(userID, in)
	})
	externalMCPTool(server, "register_shot_media_version", "登记自有图片/视频为镜头候选版本，明确源裁切起点与时长；需镜头指纹及幂等键。不采用、不裁剪文件、不改正式片；静图仅能验收静态信息，不证明动态连续性，时长变化需独立重对齐", func(_ context.Context, in service.RegisterDramaShotMediaRequest) (any, error) {
		return svc.RegisterDramaShotMedia(userID, in)
	})
	externalMCPTool(server, "review_shot_media_version", "追加picture/text/continuity人工观察。expectedShotRevision必须用list_shot_media_versions顶层当前单镜shotRevision，不用段视频take.shotRevision（整段指纹）；409返回当前expectedShotRevision及范围。expectedReviewId仅用同一takeId+shotId+layer的最新ID，每层首次传空串，不能跨层；qa.latestReviewIds提供各层ID。需完整裁切窗、原因和幂等键；不采用或生成正式视频", func(_ context.Context, in service.DramaShotMediaReviewRequest) (any, error) {
		return svc.ReviewDramaShotMedia(userID, in)
	})
}
