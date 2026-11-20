package handler

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"yingce/backend/internal/service"
)

func registerDramaAudioMCP(server *mcp.Server, svc *service.Service, userID string) {
	externalMCPTool(server, "inspect_segment_audio", "检查明确音轨版本中 speech 片段的实际文件，source=cue 为配音音频，video 为该视频时窗的真实音轨。服务端 ffprobe/ffmpeg/whisper.cpp 识别并保存文件哈希、文本和时间戳比对；缺少 ASR 返回 unknown，字级时间戳缺失为 unavailable。不会推断说话人或口型通过。需幂等键与 expectedCheckId", func(ctx context.Context, input service.InspectDramaAudioRequest) (any, error) {
		policy, err := svc.RuntimePolicy()
		if err != nil {
			return nil, err
		}
		if err := externalMCPAllow(ctx, svc, "audio-inspect:"+userID, policy.Request.TaskCreatePerMinute); err != nil {
			return nil, err
		}
		return svc.InspectDramaAudio(ctx, userID, input)
	})
	externalMCPTool(server, "record_segment_audio_qa", "对明确音轨/视频版本追加人工分层 QA：content/picture/speaker/mouth/sound_effect/final。需完整观察时间窗、依据或拒绝原因、expectedCheckId 与幂等键。内心独白须 closed；画外旁白可明确 not_visible；开口对白须 speaking 且 lipSyncChecked。最终通过前需 ASR、声源、嘴部、音效、内容和画面全部通过；不替换正式视频", func(_ context.Context, input service.DramaAudioQARequest) (any, error) {
		return svc.RecordDramaAudioQA(userID, input)
	})
	externalMCPTool(server, "get_segment_audio", "读取段/镜音轨计划、声源、文本、段内时间窗、实际任务/资源和版本；revision=0 读最新。requestedText 不代表实际语音，mouthRule 是待检查要求", func(_ context.Context, input struct {
		service.DramaAudioScope
		Revision int64 `json:"revision"`
	}) (any, error) {
		return svc.DramaSegmentAudio(userID, input.DramaAudioScope, input.Revision)
	})
	externalMCPTool(server, "save_segment_audio", "保存独立后期音轨版本；expectedAudioRevision=0 首建，需 expectedShotRevision 和幂等键。speech 模式/说话人与镜头一致，sfx 用 silent；时间为段内毫秒且须在镜头窗内。videoTakeId 可绑定本段历史视频，不替换正式视频", func(_ context.Context, input service.SaveDramaAudioRequest) (any, error) {
		return svc.SaveDramaSegmentAudio(userID, input)
	})
	externalMCPTool(server, "preview_segment_audio", "只读预检音轨、真实 TTS 准入报价、账号积分和作品剩余预算；有任务或音轨不代表台词、声源或口型验收通过", func(_ context.Context, input service.DramaAudioScope) (any, error) {
		return svc.PreviewDramaSegmentAudio(userID, input)
	})
	for _, tool := range []struct{ name, command, description string }{
		{"generate_segment_audio_cue", "generate", "按音轨计划为单个 speech 片段提交 TTS；需明确模型/音色、费用上限、音轨/分镜版本和幂等键。返回新音轨版本与 taskId，不改正式视频；用 get_segment_audio/get_task 查询"},
		{"bind_segment_audio_cue", "bind", "将已成功的配音任务实际产物或自有音频素材绑定到单个音轨片段；需音轨/分镜版本及幂等键。任务绑定时 resourceId 可省略；时长须覆盖时间窗，不做自动拉伸或替换正式视频"},
	} {
		command := tool.command
		externalMCPTool(server, tool.name, tool.description, func(ctx context.Context, input service.MutateDramaAudioCueRequest) (any, error) {
			policy, err := svc.RuntimePolicy()
			if err != nil {
				return nil, err
			}
			if err := externalMCPAllow(ctx, svc, "tasks:"+userID, policy.Request.TaskCreatePerMinute); err != nil {
				return nil, err
			}
			return svc.MutateDramaAudioCue(userID, command, input)
		})
	}
}
