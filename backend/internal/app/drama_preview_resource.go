package app

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	_ "image/jpeg"
	"math"
	"strconv"
	"strings"

	"yingce/backend/internal/model"
)

func dramaPreviewJPEGDimensions(data []byte) (int, int, error) {
	if len(data) == 0 || len(data) > 8<<20 {
		return 0, 0, BadAuthRequest("预览帧为空或超过8 MiB")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, BadAuthRequest("预览帧不是有效的JPEG")
	}
	return config.Width, config.Height, nil
}

// Persist only an extracted frame as a standalone resource. It never attaches
// to a shot/take/canvas or records a QA decision. Upload identity is bound to
// owned source media, extraction time and actual JPEG bytes for safe replay.
func (s *Service) PersistDramaSegmentPreviewFrame(userID string, frame DramaSegmentFrame) (DramaSegmentFrame, error) {
	if math.IsNaN(frame.AtSeconds) || math.IsInf(frame.AtSeconds, 0) || frame.AtSeconds < 0 || frame.AtSeconds > 3600 {
		return DramaSegmentFrame{}, BadAuthRequest("预览时间无效")
	}
	source, err := s.repo.ResourceForUser(userID, frame.ResourceID)
	if err != nil || source.Status != model.ResourceStatusReady || !strings.HasPrefix(source.MimeType, "video/") {
		return DramaSegmentFrame{}, BadAuthRequest("预览来源须为自有已就绪视频")
	}
	width, height, err := dramaPreviewJPEGDimensions(frame.JPEG)
	if err != nil {
		return DramaSegmentFrame{}, err
	}
	sum := sha256.Sum256(frame.JPEG)
	identity := "drama-preview-frame:" + dramaContentRevision([]string{source.ID, frame.TakeID, strconv.FormatFloat(frame.AtSeconds, 'f', 3, 64), fmt.Sprintf("%x", sum)})
	resource, err := s.UploadResourceFile(userID, "segment-preview.jpg", int64(len(frame.JPEG)), "image", width, height, 0, bytes.NewReader(frame.JPEG), identity)
	if err != nil {
		return DramaSegmentFrame{}, err
	}
	frame.FrameResourceID, frame.FrameWidth, frame.FrameHeight, frame.FrameMimeType = resource.ID, resource.Width, resource.Height, resource.MimeType
	return frame, nil
}
