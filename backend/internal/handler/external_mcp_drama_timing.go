package handler

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"yingce/backend/internal/service"
)

func registerDramaTimingMCP(server *mcp.Server, svc *service.Service, userID string) {
	externalMCPTool(server, "get_episode_timing", "只读按稳定 shotId 读取完整分镜绝对／段内毫秒时窗、剧本估算对白窗、已保存音轨计划与版本指纹；估算不代表实测或听审通过", func(_ context.Context, input service.DramaTimingRequest) (any, error) {
		return svc.DramaEpisodeTiming(userID, input)
	})
	externalMCPTool(server, "preview_episode_timing_edits", "仅 dryRun=true：按 shotId 批量 retime／merge_next，可合并跨段相邻表演镜；需 expectedEpisodeRevision 与 expectedTimingRevision。返回前后时窗、原句与来源、受影响段、音轨复核项和真实规格预计生成费用，不保存或生成", func(_ context.Context, input service.PreviewDramaTimingRequest) (any, error) {
		return svc.PreviewDramaTimingEdits(userID, input)
	})
}
