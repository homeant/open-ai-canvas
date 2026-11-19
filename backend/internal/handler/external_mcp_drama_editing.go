package handler

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"yingce/backend/internal/service"
)

func registerDramaEditingMCP(server *mcp.Server, svc *service.Service, userID string) {
	externalMCPTool(server, "inspect_segment_version_media", "实测本段指定 takeId 或当前版本的音视频流、编解码器和时长；音轨存在与人声、独白正确性分开返回，探测失败为 unknown", func(ctx context.Context, input struct {
		DramaID      string `json:"dramaId"`
		EpisodeID    string `json:"episodeId"`
		SegmentIndex int    `json:"segmentIndex"`
		TakeID       string `json:"takeId,omitempty"`
	}) (any, error) {
		return svc.InspectDramaSegmentVersionMedia(ctx, userID, input.DramaID, input.EpisodeID, input.SegmentIndex, input.TakeID)
	})
	externalMCPTool(server, "get_episode_source", "读取剧集原文章节、素材类型、独立 sourceRevision 与分镜 episodeRevision；默认保存不会重新改编", func(_ context.Context, input struct {
		DramaID   string `json:"dramaId"`
		EpisodeID string `json:"episodeId"`
	}) (any, error) {
		return svc.DramaEpisodeSource(userID, input.DramaID, input.EpisodeID)
	})
	externalMCPTool(server, "update_episode_source", "按 expectedSourceRevision 保存原文；readapt 默认为 false，明确为 true 才清空分镜并排队改编。需要幂等键", func(_ context.Context, input struct {
		DramaID   string `json:"dramaId"`
		EpisodeID string `json:"episodeId"`
		service.UpdateDramaEpisodeSourceRequest
	}) (any, error) {
		return svc.UpdateDramaEpisodeSource(userID, input.DramaID, input.EpisodeID, input.UpdateDramaEpisodeSourceRequest)
	})
	externalMCPTool(server, "edit_episode_shots", "按 shotId 原子 insert_after/update/delete；支持 scene/sceneCastId/castIds/propIds/lookCastIds（空数组清除），重绑场景默认同步场景名。需 expectedEpisodeRevision 与幂等键；插镜须 durationSeconds，newShotId 可同批定位。绑定变化使提示词待复核", func(_ context.Context, input struct {
		DramaID   string `json:"dramaId"`
		EpisodeID string `json:"episodeId"`
		service.EditDramaShotsRequest
	}) (any, error) {
		return svc.EditDramaShots(userID, input.DramaID, input.EpisodeID, input.EditDramaShotsRequest)
	})
	externalMCPTool(server, "edit_segment_references", "修改段级图片选择；需 expectedSegmentRevision、expectedShotRevision、幂等键。referenceCastIds 按顺序指定设定，[] 清空；autoReferences=true 恢复镜头自动引用；referenceImages:[] 清除额外图片。不会自动生成或修改镜头绑定，修改后须复核提示词", func(_ context.Context, input service.DramaMCPReferenceRequest) (any, error) {
		return svc.EditDramaSegmentReferencesMCP(userID, input)
	})
	for _, command := range []struct{ name, action, description string }{
		{"rewrite_segment_prompt", "rewrite", "异步按当前分镜重写；model 可选择文本模型。返回 jobId，使用 get_drama_job 等候 taskId，再用 get_task 查询。候选必须显式采用。需段版本、分镜版本、幂等键"},
		{"edit_segment_prompt", "edit", "保存人工完整提示词并标为待核对，不发起生成；需段版本、分镜版本、当前视频 model、expectedSeconds 和幂等键。之后必须 confirm_segment_prompt"},
		{"adopt_segment_prompt", "adopt", "按 candidateId 采用与当前分镜绑定的候选提示词；需段版本、分镜版本、当前视频 model、expectedSeconds 和幂等键"},
		{"confirm_segment_prompt", "confirm", "显式核对人工提示词与现行分镜、视频模型及时长档位后确认生效；需段版本、分镜版本、当前视频 model、expectedSeconds 和幂等键"},
	} {
		action := command.action
		externalMCPTool(server, command.name, command.description, func(_ context.Context, input service.DramaMCPPromptRequest) (any, error) {
			return svc.MutateDramaSegmentPrompt(userID, action, input)
		})
	}
	externalMCPTool(server, "get_drama_job", "读取异步短剧作业、实际 taskId、状态和模型返回；rewrite_segment_prompt 派发前没有 taskId", func(_ context.Context, input struct {
		DramaID string `json:"dramaId"`
		JobID   string `json:"jobId"`
	}) (any, error) {
		return svc.DramaJobDetail(userID, input.DramaID, input.JobID)
	})
	externalMCPTool(server, "preview_segment_generation", "只读预检实际生成请求、模型、参考图、完整提示词、时长档位、报价失败原因、阻塞项和声音能力；未知音轨与人声状态不会当作合格独白", func(_ context.Context, input struct {
		DramaID      string `json:"dramaId"`
		EpisodeID    string `json:"episodeId"`
		SegmentIndex int    `json:"segmentIndex"`
	}) (any, error) {
		return svc.PreviewDramaSegmentGeneration(userID, input.DramaID, input.EpisodeID, input.SegmentIndex)
	})
}
