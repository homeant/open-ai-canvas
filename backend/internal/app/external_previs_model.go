package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"

	"yingce/backend/internal/repository"
)

const externalPrevisMCPModelLimit = 2 << 20

type ExternalPrevisModelUploadRequest struct {
	FileName       string `json:"fileName"`
	DataBase64     string `json:"dataBase64"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type externalPrevisGLBNode struct {
	Name     string `json:"name"`
	Mesh     *int   `json:"mesh"`
	Children []int  `json:"children"`
}

func externalPrevisParseGLB(data []byte) ([]externalPrevisGLBNode, error) {
	if len(data) < 20 || string(data[:4]) != "glTF" || binary.LittleEndian.Uint32(data[4:8]) != 2 || int(binary.LittleEndian.Uint32(data[8:12])) != len(data) {
		return nil, BadAuthRequest("模型必须是完整的 GLB 2.0 文件")
	}
	jsonLength := int(binary.LittleEndian.Uint32(data[12:16]))
	if string(data[16:20]) != "JSON" || jsonLength < 2 || jsonLength > 1<<20 || 20+jsonLength > len(data) {
		return nil, BadAuthRequest("GLB JSON 分块无效或过大")
	}
	var doc struct {
		Nodes  []externalPrevisGLBNode `json:"nodes"`
		Scenes []struct {
			Nodes []int `json:"nodes"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(bytes.TrimRight(data[20:20+jsonLength], " \x00"), &doc); err != nil || len(doc.Nodes) > 512 {
		return nil, BadAuthRequest("GLB 节点结构无效或过大")
	}
	for _, node := range doc.Nodes {
		for _, child := range node.Children {
			if child < 0 || child >= len(doc.Nodes) {
				return nil, BadAuthRequest("GLB 子节点索引无效")
			}
		}
	}
	return doc.Nodes, nil
}

func (s *Service) UploadExternalPrevisModel(userID string, req ExternalPrevisModelUploadRequest) (map[string]any, error) {
	if err := externalPrevisKey(req.IdempotencyKey); err != nil {
		return nil, err
	}
	if err := cloudAgentPrevisValidateText(req.FileName, "模型文件名", 160, true); err != nil {
		return nil, err
	}
	if !strings.HasSuffix(strings.ToLower(req.FileName), ".glb") {
		return nil, BadAuthRequest("模型文件名必须以 .glb 结尾")
	}
	if len(req.DataBase64) > base64.StdEncoding.EncodedLen(externalPrevisMCPModelLimit) {
		return nil, BadAuthRequest("MCP 单次上传最多 2 MiB；大文件请先通过 HTTPS 导入资源")
	}
	data, err := base64.StdEncoding.DecodeString(req.DataBase64)
	if err != nil || len(data) == 0 || len(data) > externalPrevisMCPModelLimit {
		return nil, BadAuthRequest("模型 base64 内容无效或过大")
	}
	nodes, err := externalPrevisParseGLB(data)
	if err != nil {
		return nil, err
	}
	resource, err := s.UploadResourceFile(userID, req.FileName, int64(len(data)), "file", 0, 0, 0, bytes.NewReader(data), "mcp-previs-model:"+userID+":"+req.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if err := s.externalPrevisVerifyResourceBytes(userID, resource.ID, data); err != nil {
		return nil, err
	}
	return map[string]any{"resourceId": resource.ID, "storageKey": "resource:" + resource.ID, "bytes": resource.Size, "nodes": externalPrevisNodeSummaries(nodes)}, nil
}

func (s *Service) externalPrevisVerifyResourceBytes(userID, resourceID string, data []byte) error {
	_, body, err := s.OpenResource(userID, resourceID)
	if err != nil {
		return err
	}
	defer body.Close()
	stored, err := io.ReadAll(io.LimitReader(body, int64(len(data))+1))
	if err != nil {
		return err
	}
	if sha256.Sum256(stored) != sha256.Sum256(data) {
		return creationConflict("上传幂等标识已用于其他文件内容")
	}
	return nil
}

func externalPrevisNodeSummaries(nodes []externalPrevisGLBNode) []map[string]any {
	result := make([]map[string]any, 0, len(nodes))
	for index, node := range nodes {
		if node.Mesh != nil {
			result = append(result, map[string]any{"index": index, "name": node.Name, "mesh": *node.Mesh, "children": node.Children})
		}
	}
	return result
}

func (s *Service) ExternalPrevisModelNodes(userID, resourceID string) (map[string]any, error) {
	if err := validateCloudAgentID(resourceID, "resourceId", 80); err != nil {
		return nil, err
	}
	resource, body, err := s.OpenResource(userID, resourceID)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	if resource.Size > 64<<20 {
		return nil, BadAuthRequest("GLB 超过 64 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(body, resource.Size+1))
	if err != nil {
		return nil, err
	}
	nodes, err := externalPrevisParseGLB(data)
	if err != nil {
		return nil, err
	}
	return map[string]any{"resourceId": resourceID, "bytes": resource.Size, "nodes": externalPrevisNodeSummaries(nodes)}, nil
}

func cloudAgentPrevisValidateExternalReferences(repo *repository.Repository, userID, dramaID, episodeID string, operation cloudAgentPrevisPatchOperation) error {
	if operation.ResourceID != nil {
		if err := validateCloudAgentID(*operation.ResourceID, "resourceId", 80); err != nil {
			return err
		}
		resource, err := repo.ResourceForUser(userID, *operation.ResourceID)
		if err != nil {
			return dramaLookupError(err)
		}
		if resource.Status != "ready" {
			return BadAuthRequest("模型资源尚未上传完成")
		}
	}
	if operation.RenderResourceID != nil || operation.RenderKind != nil {
		if operation.Type != "shot_render_set" || operation.RenderResourceID == nil || operation.RenderKind == nil {
			return BadAuthRequest("预览资源字段仅用于 shot_render_set")
		}
		if err := validateCloudAgentID(*operation.RenderResourceID, "renderResourceId", 80); err != nil {
			return err
		}
		resource, err := repo.ResourceForUser(userID, *operation.RenderResourceID)
		if err != nil {
			return dramaLookupError(err)
		}
		expected := "image"
		if *operation.RenderKind == "video" {
			expected = "video"
		} else if *operation.RenderKind != "frame" {
			return BadAuthRequest("renderKind 不受支持")
		}
		if resource.Status != "ready" || resource.Kind != expected {
			return BadAuthRequest("预览资源类型不匹配或尚未就绪")
		}
	}
	if operation.GLTFNodeIndex != nil && (operation.ResourceID == nil || *operation.GLTFNodeIndex < 0 || *operation.GLTFNodeIndex > 511) {
		return BadAuthRequest("gltfNodeIndex 需要有效的 resourceId")
	}
	if operation.DramaCastID != nil {
		if err := validateCloudAgentID(*operation.DramaCastID, "dramaCastId", 80); err != nil {
			return err
		}
		if dramaID == "" {
			return BadAuthRequest("角色绑定需要作品画布")
		}
		if _, err := repo.DramaCast(dramaID, *operation.DramaCastID); err != nil {
			return dramaLookupError(err)
		}
	}
	if operation.DramaShotID != nil {
		if err := validateCloudAgentID(*operation.DramaShotID, "dramaShotId", 80); err != nil {
			return err
		}
		if dramaID == "" || episodeID == "" {
			return BadAuthRequest("镜头关联需要作品和剧集")
		}
		shot, err := repo.DramaShot(dramaID, *operation.DramaShotID)
		if err != nil {
			return dramaLookupError(err)
		}
		if shot.EpisodeID != episodeID {
			return Forbidden("镜头属于其他剧集")
		}
	}
	if operation.AspectRatio != nil {
		if err := cloudAgentPrevisValidateEnum(*operation.AspectRatio, "aspectRatio", "auto", "2.39:1", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"); err != nil {
			return err
		}
	}
	return nil
}
