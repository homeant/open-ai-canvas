package handler

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"yingce/backend/internal/service"
)

func registerMediaUploadMCP(server *mcp.Server, svc *service.Service, userID string) {
	externalMCPTool(server, "begin_media_upload", "开始可重试的本地 PNG/JPEG/MP4/WAV 分片上传；需要文件名、类型、大小、完整 SHA-256 和幂等键。返回1 MiB分片大小和90分钟期限；上传不代表采用或验收", func(_ context.Context, in service.ExternalUploadBeginRequest) (any, error) {
		return svc.BeginExternalMediaUpload(userID, in)
	})
	externalMCPTool(server, "upload_media_chunk", "上传单个最多1 MiB的 Base64 分片；index从0开始，必须提供分片 SHA-256，同序号不同内容拒绝；请勿把文件内容或凭据写入日志", func(_ context.Context, in service.ExternalUploadChunkRequest) (any, error) {
		return svc.PutExternalMediaUploadChunk(userID, in)
	})
	for _, action := range []struct{ name, description string }{
		{"get_media_upload", "读取上传进度、已有分片哈希与资源ID；不会读取文件内容"},
		{"complete_media_upload", "合并并验证完整大小/SHA-256/真实媒体类型与尺寸时长，保存一个自有资源；同一上传重试返回相同资源，不建立画布节点、不采用到正式作品"},
		{"abort_media_upload", "取消未完成的上传并释放配额、删除该会话分片；不会删除已完成资源"},
	} {
		externalMCPTool(server, action.name, action.description, func(_ context.Context, in struct {
			UploadID string `json:"uploadId"`
		}) (any, error) {
			switch action.name {
			case "complete_media_upload":
				return svc.CompleteExternalMediaUpload(userID, in.UploadID)
			case "abort_media_upload":
				return svc.AbortExternalMediaUpload(userID, in.UploadID)
			default:
				return svc.ExternalMediaUploadStatus(userID, in.UploadID)
			}
		})
	}
}
