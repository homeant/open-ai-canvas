package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"yingce/backend/internal/model"
)

const externalUploadChunkSize = 1 << 20

var externalUploadLocks [64]sync.Mutex

type ExternalUploadBeginRequest struct {
	FileName       string `json:"fileName"`
	Kind           string `json:"kind"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	IdempotencyKey string `json:"idempotencyKey"`
}
type ExternalUploadChunkRequest struct {
	UploadID   string `json:"uploadId"`
	Index      int    `json:"index"`
	DataBase64 string `json:"dataBase64"`
	SHA256     string `json:"sha256"`
}
type externalUploadManifest struct {
	ID         string         `json:"uploadId"`
	Owner      string         `json:"owner"`
	Digest     string         `json:"digest"`
	FileName   string         `json:"fileName"`
	Kind       string         `json:"kind"`
	Size       int64          `json:"size"`
	SHA256     string         `json:"sha256"`
	ChunkSize  int64          `json:"chunkSize"`
	ChunkCount int            `json:"chunkCount"`
	Chunks     map[int]string `json:"chunks"`
	ExpiresAt  time.Time      `json:"expiresAt"`
	Status     string         `json:"status"`
	ResourceID string         `json:"resourceId,omitempty"`
}

func validExternalSHA(s string) bool {
	data, err := hex.DecodeString(s)
	return err == nil && len(data) == 32
}
func externalUploadLock(id string) *sync.Mutex {
	sum := sha256.Sum256([]byte(id))
	return &externalUploadLocks[int(sum[0])%len(externalUploadLocks)]
}
func (s *Service) externalUploadDir(userID, id string) (string, error) {
	if len(id) != 32 {
		return "", BadAuthRequest("上传 ID 无效")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", BadAuthRequest("上传 ID 无效")
	}
	owner := dramaContentRevision(userID)
	return filepath.Join(s.dataDir, "staging", "mcp-uploads", owner, id), nil
}
func saveExternalUploadManifest(dir string, m externalUploadManifest) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "manifest.json"))
}
func (s *Service) loadExternalUpload(userID, id string) (externalUploadManifest, string, error) {
	var m externalUploadManifest
	dir, err := s.externalUploadDir(userID, id)
	if err != nil {
		return m, "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, dir, NotFound("上传会话不存在")
	}
	if err != nil {
		return m, dir, err
	}
	if err = json.Unmarshal(data, &m); err != nil {
		return m, dir, err
	}
	if m.Owner != dramaContentRevision(userID) || m.ID != id {
		return m, dir, Forbidden("上传会话归属无效")
	}
	return m, dir, nil
}
func externalUploadResult(m externalUploadManifest) map[string]any {
	return map[string]any{"uploadId": m.ID, "status": m.Status, "size": m.Size, "sha256": m.SHA256, "chunkSize": m.ChunkSize, "chunkCount": m.ChunkCount, "receivedChunks": m.Chunks, "expiresAt": m.ExpiresAt, "resourceId": m.ResourceID, "uploaded": m.Status == "completed", "adopted": false}
}

func (s *Service) BeginExternalMediaUpload(userID string, req ExternalUploadBeginRequest) (map[string]any, error) {
	if user, err := s.repo.User(userID); err != nil || user.Status != model.UserStatusActive {
		return nil, Forbidden("上传用户不存在或不可用")
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 200 {
		return nil, BadAuthRequest("上传需要1–200字节幂等键")
	}
	if req.FileName == "" || len(req.FileName) > 255 || strings.ContainsAny(req.FileName, "/\\\x00\r\n") {
		return nil, BadAuthRequest("文件名无效")
	}
	if req.Kind != "image" && req.Kind != "video" && req.Kind != "audio" {
		return nil, BadAuthRequest("仅支持图片、视频、音频")
	}
	req.SHA256 = strings.ToLower(req.SHA256)
	if !validExternalSHA(req.SHA256) {
		return nil, BadAuthRequest("需要完整文件 SHA-256")
	}
	if req.Size <= 0 || req.Size > 2<<30 {
		return nil, BadAuthRequest("文件大小必须在1字节至2 GiB内")
	}
	id := dramaContentRevision([]string{userID, "mcp-upload", req.IdempotencyKey})[:32]
	lock := externalUploadLock(id)
	lock.Lock()
	defer lock.Unlock()
	digest := dramaContentRevision(req)
	m, dir, err := s.loadExternalUpload(userID, id)
	if err == nil {
		if m.Digest != digest {
			return nil, creationConflict("幂等键已用于不同文件")
		}
		if m.Status == "receiving" && time.Now().After(m.ExpiresAt) {
			return nil, BadAuthRequest("上传已过期，请使用新幂等键")
		}
		return externalUploadResult(m), nil
	}
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Status != 404 {
		return nil, err
	}
	expires := time.Now().Add(90 * time.Minute)
	if err := s.ReserveChunkUploadSession(userID, id, req.Size, expires, 32); err != nil {
		return nil, err
	}
	m = externalUploadManifest{ID: id, Owner: dramaContentRevision(userID), Digest: digest, FileName: req.FileName, Kind: req.Kind, Size: req.Size, SHA256: req.SHA256, ChunkSize: externalUploadChunkSize, ChunkCount: int((req.Size + externalUploadChunkSize - 1) / externalUploadChunkSize), Chunks: map[int]string{}, ExpiresAt: expires, Status: "receiving"}
	if err := saveExternalUploadManifest(dir, m); err != nil {
		_ = s.ReleaseChunkUploadSession(userID, id)
		return nil, err
	}
	return externalUploadResult(m), nil
}

func (s *Service) ExternalMediaUploadStatus(userID, id string) (map[string]any, error) {
	lock := externalUploadLock(id)
	lock.Lock()
	defer lock.Unlock()
	m, _, err := s.loadExternalUpload(userID, id)
	if err != nil {
		return nil, err
	}
	return externalUploadResult(m), nil
}

func (s *Service) PutExternalMediaUploadChunk(userID string, req ExternalUploadChunkRequest) (map[string]any, error) {
	if len(req.DataBase64) > base64.StdEncoding.EncodedLen(externalUploadChunkSize) {
		return nil, BadAuthRequest("单片最多1 MiB")
	}
	data, err := base64.StdEncoding.DecodeString(req.DataBase64)
	if err != nil {
		return nil, BadAuthRequest("分片 Base64 无效")
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if !validExternalSHA(req.SHA256) || !strings.EqualFold(req.SHA256, actual) {
		return nil, BadAuthRequest("分片 SHA-256 不匹配")
	}
	lock := externalUploadLock(req.UploadID)
	lock.Lock()
	defer lock.Unlock()
	m, dir, err := s.loadExternalUpload(userID, req.UploadID)
	if err != nil {
		return nil, err
	}
	if m.Status != "receiving" || time.Now().After(m.ExpiresAt) {
		return nil, BadAuthRequest("上传会话已完成、取消或过期")
	}
	if req.Index < 0 || req.Index >= m.ChunkCount {
		return nil, BadAuthRequest("分片序号无效")
	}
	expected := m.ChunkSize
	if req.Index == m.ChunkCount-1 {
		expected = m.Size - int64(req.Index)*m.ChunkSize
	}
	if int64(len(data)) != expected {
		return nil, BadAuthRequest("分片长度不符")
	}
	if old, exists := m.Chunks[req.Index]; exists && old != actual {
		return nil, creationConflict("该分片序号已绑定其他内容")
	}
	path := filepath.Join(dir, fmt.Sprintf("part-%d", req.Index))
	f, err := os.CreateTemp(dir, "part-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return nil, err
	}
	m.Chunks[req.Index] = actual
	if err = saveExternalUploadManifest(dir, m); err != nil {
		return nil, err
	}
	return map[string]any{"uploadId": m.ID, "index": req.Index, "receivedBytes": len(data), "receivedCount": len(m.Chunks), "chunkCount": m.ChunkCount, "sha256": actual}, nil
}

func (s *Service) CompleteExternalMediaUpload(userID, id string) (map[string]any, error) {
	lock := externalUploadLock(id)
	lock.Lock()
	defer lock.Unlock()
	m, dir, err := s.loadExternalUpload(userID, id)
	if err != nil {
		return nil, err
	}
	if m.Status == "completed" {
		resource, err := s.repo.ResourceForUser(userID, m.ResourceID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"upload": externalUploadResult(m), "resource": resource, "adopted": false}, nil
	}
	if m.Status != "receiving" || time.Now().After(m.ExpiresAt) {
		return nil, BadAuthRequest("上传已取消或过期")
	}
	if len(m.Chunks) != m.ChunkCount {
		return nil, BadAuthRequest("分片不完整")
	}
	f, err := os.OpenFile(filepath.Join(dir, "merged"), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hash := sha256.New()
	total := int64(0)
	for i := 0; i < m.ChunkCount; i++ {
		part, err := os.Open(filepath.Join(dir, fmt.Sprintf("part-%d", i)))
		if err != nil {
			return nil, err
		}
		n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(part, m.ChunkSize+1))
		part.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		total += n
	}
	if total != m.Size || hex.EncodeToString(hash.Sum(nil)) != m.SHA256 {
		return nil, BadAuthRequest("完整文件大小或 SHA-256 不匹配，未保存资源")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	mime, err := detectUploadedMimeType(f)
	if err != nil {
		return nil, err
	}
	if uploadedResourceKind(mime) != m.Kind {
		return nil, BadAuthRequest("实际文件类型与上传声明不一致")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	width, height, duration := 0, 0, int64(0)
	if m.Kind == "image" {
		info, _, probeErr := image.DecodeConfig(f)
		if probeErr != nil {
			return nil, BadAuthRequest("图片解码失败；目前支持 PNG/JPEG")
		}
		width, height = info.Width, info.Height
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		info, probeErr := probeMediaFile(ctx, f.Name())
		cancel()
		if probeErr != nil {
			return nil, NewAppError(422, "媒体实测失败，请检查文件和服务端 ffprobe")
		}
		width, height, duration = info.Width, info.Height, info.DurationMs
		if duration <= 0 || (m.Kind == "video" && (width <= 0 || height <= 0)) {
			return nil, BadAuthRequest("媒体没有有效时长或视频尺寸")
		}
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// Failed storage attempts release their reservation. Reacquire the same lease
	// before retrying; the resource upload identity still prevents duplicate rows.
	identity := "mcp-upload:" + id + ":" + m.SHA256
	existing, err := s.resourceForUploadKey(userID, normalizedResourceUploadKey([]string{identity}))
	if err != nil {
		return nil, err
	}
	if existing == nil || existing.Status != model.ResourceStatusReady {
		if err := s.ReleaseChunkUploadSession(userID, id); err != nil {
			return nil, err
		}
		if err := s.ReserveChunkUploadSession(userID, id, m.Size, m.ExpiresAt, 32); err != nil {
			return nil, err
		}
	}
	resource, err := s.UploadReservedResourceFile(userID, id, m.FileName, m.Size, m.Kind, width, height, duration, f, identity)
	if err != nil {
		return nil, err
	}
	m.Status, m.ResourceID = "completed", resource.ID
	if err := saveExternalUploadManifest(dir, m); err != nil {
		return nil, err
	}
	f.Close()
	_ = os.Remove(filepath.Join(dir, "merged"))
	for i := 0; i < m.ChunkCount; i++ {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("part-%d", i)))
	}
	return map[string]any{"upload": externalUploadResult(m), "resource": resource, "adopted": false}, nil
}

func (s *Service) AbortExternalMediaUpload(userID, id string) (map[string]any, error) {
	lock := externalUploadLock(id)
	lock.Lock()
	defer lock.Unlock()
	m, dir, err := s.loadExternalUpload(userID, id)
	if err != nil {
		return nil, err
	}
	if m.Status == "completed" {
		return nil, BadAuthRequest("已完成资源不能通过取消上传删除")
	}
	if err := s.ReleaseChunkUploadSession(userID, id); err != nil {
		return nil, err
	}
	m.Status = "aborted"
	if err := saveExternalUploadManifest(dir, m); err != nil {
		return nil, err
	}
	for i := 0; i < m.ChunkCount; i++ {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("part-%d", i)))
	}
	_ = os.Remove(filepath.Join(dir, "merged"))
	return externalUploadResult(m), nil
}
