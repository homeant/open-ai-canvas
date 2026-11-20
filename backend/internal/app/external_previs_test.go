package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"testing"

	"yingce/backend/internal/model"
)

func TestExternalPrevisMCPScopedMutationAndReplay(t *testing.T) {
	s, db, _, _ := creationTestService(t)
	for _, row := range []any{
		&model.Project{ID: "drama-1", UserID: "user", Name: "重回2013", Type: "drama"},
		&model.DramaProduction{ProjectID: "drama-1", UserID: "user"},
		&model.DramaEpisode{ID: "episode-1", ProjectID: "drama-1", Number: 1},
		&model.DramaEpisode{ID: "episode-2", ProjectID: "drama-1", Number: 2},
		&model.CanvasProject{ID: "canvas-1", UserID: "user", ProjectID: "drama-1", Title: "精修画布", PayloadJSON: `{"nodes":[],"connections":[],"previsScenes":[]}`},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	scope := ExternalPrevisScope{DramaID: "drama-1", EpisodeID: "episode-1", CanvasID: "canvas-1"}
	listed, err := s.ListExternalPrevisScenes("user", scope)
	if err != nil {
		t.Fatal(err)
	}
	canvases := listed["canvases"].([]map[string]any)
	if len(canvases) != 1 || len(listed["scenes"].([]map[string]any)) != 0 {
		t.Fatalf("unexpected empty catalog: %#v", listed)
	}
	create := ExternalPrevisCreateRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, CanvasSnapshotHash: canvases[0]["canvasSnapshotHash"].(string), IdempotencyKey: "fruit-shop-01", Title: "EP01｜老冯水果店及门口｜待审", TemplateID: "empty"}
	create.DryRun = true
	planned, err := s.CreateExternalPrevisScene("user", create)
	if err != nil || planned["committed"] != false {
		t.Fatalf("create dry-run: %#v %v", planned, err)
	}
	create.DryRun = false
	created, err := s.CreateExternalPrevisScene("user", create)
	if err != nil || created["committed"] != true {
		t.Fatalf("create: %#v %v", created, err)
	}
	replayed, err := s.CreateExternalPrevisScene("user", create)
	if err != nil || replayed["sceneId"] != created["sceneId"] || replayed["snapshotHash"] != created["snapshotHash"] {
		t.Fatalf("create replay: %#v %v", replayed, err)
	}
	changed := create
	changed.Title = "不同的场景"
	if _, err := s.CreateExternalPrevisScene("user", changed); err == nil {
		t.Fatal("same idempotency key accepted different content")
	}
	if _, err := s.ListExternalPrevisScenes("other", scope); err == nil {
		t.Fatal("cross-user list accepted")
	}
	if _, err := s.GetExternalPrevisScene("user", ExternalPrevisReadRequest{DramaID: "drama-1", EpisodeID: "episode-2", CanvasID: "canvas-1", SceneID: created["sceneId"].(string)}); err == nil {
		t.Fatal("cross-episode scene read accepted")
	}
	read := ExternalPrevisReadRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, SceneID: created["sceneId"].(string), IncludeTransforms: true}
	current, err := s.GetExternalPrevisScene("user", read)
	if err != nil || current["snapshotHash"] != created["snapshotHash"] || current["nodeId"] != created["nodeId"] {
		t.Fatalf("read: %#v %v", current, err)
	}
	name := "收银台"
	position := []float64{-1.13, 0.45, -1.80}
	scale := []float64{0.60, 0.90, 1.40}
	patch := ExternalPrevisPatchRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: read.SceneID, SnapshotHash: current["snapshotHash"].(string), IdempotencyKey: "fruit-counter-01", Operations: []ExternalPrevisPatchOperation{{Type: "object_add", ID: "counter", Name: &name, Kind: "primitive", Primitive: "box", Position: &position, Scale: &scale}}}
	patch.DryRun = true
	if result, err := s.ApplyExternalPrevisPatch("user", patch); err != nil || result["committed"] != false {
		t.Fatalf("patch dry-run: %#v %v", result, err)
	}
	patch.DryRun = false
	patched, err := s.ApplyExternalPrevisPatch("user", patch)
	if err != nil || patched["committed"] != true {
		t.Fatalf("patch: %#v %v", patched, err)
	}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err != nil {
		t.Fatalf("patch replay: %v", err)
	}
	patch.IdempotencyKey = "fruit-counter-02"
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err == nil {
		t.Fatal("stale scene hash accepted")
	}
	current, err = s.GetExternalPrevisScene("user", read)
	if err != nil {
		t.Fatal(err)
	}
	layout := current["scene"].(map[string]any)["spatialLayout"].(map[string]any)
	objects := layout["objects"].([]map[string]any)
	if len(objects) != 1 || objects[0]["id"] != "counter" {
		t.Fatalf("object not readable: %#v", objects)
	}
	if _, err := s.ApplyExternalPrevisPatch("user", ExternalPrevisPatchRequest{DramaID: scope.DramaID, EpisodeID: "episode-2", CanvasID: scope.CanvasID, SceneID: read.SceneID, SnapshotHash: current["snapshotHash"].(string), IdempotencyKey: "cross-episode-01", Operations: patch.Operations}); err == nil {
		t.Fatal("cross-episode patch accepted")
	}
}

func TestExternalPrevisHeadlessIntegration(t *testing.T) {
	url := os.Getenv("CANVAS_PREVIS_RENDER_TEST_URL")
	if url == "" {
		t.Skip("requires a running Vite or web server and local Chrome")
	}
	t.Setenv("CANVAS_PREVIS_RENDER_URL", url)
	s, db, _, _ := creationTestService(t)
	for _, row := range []any{&model.Project{ID: "drama-1", UserID: "user", Name: "EP01", Type: "drama"}, &model.DramaProduction{ProjectID: "drama-1", UserID: "user"}, &model.DramaEpisode{ID: "episode-1", ProjectID: "drama-1", Number: 1}, &model.CanvasProject{ID: "canvas-1", UserID: "user", ProjectID: "drama-1", Title: "预演", PayloadJSON: `{"nodes":[],"connections":[],"previsScenes":[]}`}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	scope := ExternalPrevisScope{DramaID: "drama-1", EpisodeID: "episode-1", CanvasID: "canvas-1"}
	listed, err := s.ListExternalPrevisScenes("user", scope)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateExternalPrevisScene("user", ExternalPrevisCreateRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, CanvasSnapshotHash: listed["canvases"].([]map[string]any)[0]["canvasSnapshotHash"].(string), IdempotencyKey: "render-scene-01", Title: "空场", TemplateID: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	req := ExternalPrevisRenderRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: created["sceneId"].(string), ShotID: created["shotId"].(string), SnapshotHash: created["snapshotHash"].(string), IdempotencyKey: "render-frame-01", Kind: "frame"}
	length := 320
	req.LongEdge = &length
	frame, err := s.RenderExternalPrevis(context.Background(), "user", req)
	if err != nil {
		t.Fatal(err)
	}
	if frame["kind"] != "frame" || frame["resourceId"] == "" {
		t.Fatalf("frame: %#v", frame)
	}
	replay, err := s.RenderExternalPrevis(context.Background(), "user", req)
	if err != nil || replay["resourceId"] != frame["resourceId"] {
		t.Fatalf("headless render replay: %#v %v", replay, err)
	}
	changed := req
	later := 0.5
	changed.Time = &later
	if _, err := s.RenderExternalPrevis(context.Background(), "user", changed); err == nil {
		t.Fatal("same render key accepted different time")
	}
	req.Kind = "video"
	req.IdempotencyKey = "render-video-01"
	req.SnapshotHash = frame["snapshotHash"].(string)
	duration := 1.0
	fps := 6
	req.Duration = &duration
	req.FPS = &fps
	video, err := s.RenderExternalPrevis(context.Background(), "user", req)
	if err != nil {
		t.Fatal(err)
	}
	if video["kind"] != "video" || video["resourceId"] == "" {
		t.Fatalf("video: %#v", video)
	}
	read, err := s.GetExternalPrevisRender("user", ExternalPrevisReadRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: req.SceneID, ShotID: req.ShotID})
	if err != nil || read["resourceId"] != video["resourceId"] {
		t.Fatalf("render readback: %#v %v", read, err)
	}
	if path := os.Getenv("CANVAS_PREVIS_RENDER_TEST_GLB"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		upload, err := s.UploadExternalPrevisModel("user", ExternalPrevisModelUploadRequest{FileName: "scene.glb", DataBase64: base64.StdEncoding.EncodeToString(data), IdempotencyKey: "render-model-upload-01"})
		if err != nil {
			t.Fatal(err)
		}
		resourceID := upload["resourceId"].(string)
		name := "模型"
		index := 1
		patch, err := s.ApplyExternalPrevisPatch("user", ExternalPrevisPatchRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: req.SceneID, SnapshotHash: video["snapshotHash"].(string), IdempotencyKey: "render-model-patch-01", Operations: []ExternalPrevisPatchOperation{{Type: "object_add", ID: "model-node", Name: &name, Kind: "model", ResourceID: &resourceID, GLTFNodeIndex: &index}}})
		if err != nil {
			t.Fatal(err)
		}
		req.Kind = "frame"
		req.IdempotencyKey = "render-model-frame-01"
		req.SnapshotHash = patch["snapshotHash"].(string)
		req.Duration = nil
		req.FPS = nil
		modelFrame, err := s.RenderExternalPrevis(context.Background(), "user", req)
		if err != nil || modelFrame["resourceId"] == "" {
			t.Fatalf("render imported GLB: %#v %v", modelFrame, err)
		}
		req.Kind = "video"
		req.IdempotencyKey = "render-model-video-01"
		req.SnapshotHash = modelFrame["snapshotHash"].(string)
		req.Duration = &duration
		req.FPS = &fps
		modelVideo, err := s.RenderExternalPrevis(context.Background(), "user", req)
		if err != nil || modelVideo["resourceId"] == "" {
			t.Fatalf("record imported GLB: %#v %v", modelVideo, err)
		}
	}
}

func TestExternalPrevisModelGLBInspection(t *testing.T) {
	document := []byte(`{"asset":{"version":"2.0"},"nodes":[{"name":"door","mesh":0},{"name":"room","children":[0]}],"meshes":[{"primitives":[]}],"scenes":[{"nodes":[1]}],"scene":0}`)
	for len(document)%4 != 0 {
		document = append(document, ' ')
	}
	data := make([]byte, 20+len(document))
	copy(data[:4], "glTF")
	binary.LittleEndian.PutUint32(data[4:8], 2)
	binary.LittleEndian.PutUint32(data[8:12], uint32(len(data)))
	binary.LittleEndian.PutUint32(data[12:16], uint32(len(document)))
	copy(data[16:20], "JSON")
	copy(data[20:], document)
	nodes, err := externalPrevisParseGLB(data)
	if err != nil || len(nodes) != 2 || nodes[0].Name != "door" || len(externalPrevisNodeSummaries(nodes)) != 1 {
		t.Fatalf("inspect GLB: %#v %v", nodes, err)
	}
	s, db, _, _ := creationTestService(t)
	upload := ExternalPrevisModelUploadRequest{FileName: "door.glb", DataBase64: base64.StdEncoding.EncodeToString(data), IdempotencyKey: "upload-door-01"}
	stored, err := s.UploadExternalPrevisModel("user", upload)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.ExternalPrevisModelNodes("user", stored["resourceId"].(string))
	if err != nil || len(read["nodes"].([]map[string]any)) != 1 {
		t.Fatalf("stored model inspection: %#v %v", read, err)
	}
	replayed, err := s.UploadExternalPrevisModel("user", upload)
	if err != nil || replayed["resourceId"] != stored["resourceId"] {
		t.Fatalf("model upload replay: %#v %v", replayed, err)
	}
	changed := append([]byte(nil), data...)
	changed[bytes.Index(changed, []byte("door"))] = 'b'
	upload.DataBase64 = base64.StdEncoding.EncodeToString(changed)
	if _, err := s.UploadExternalPrevisModel("user", upload); err == nil {
		t.Fatal("same upload key accepted different same-size GLB")
	}
	for _, row := range []any{&model.Project{ID: "drama-1", UserID: "user", Name: "EP01", Type: "drama"}, &model.DramaProduction{ProjectID: "drama-1", UserID: "user"}, &model.DramaEpisode{ID: "episode-1", ProjectID: "drama-1", Number: 1}, &model.CanvasProject{ID: "canvas-1", UserID: "user", ProjectID: "drama-1", Title: "预演画布", PayloadJSON: `{"nodes":[],"connections":[],"previsScenes":[]}`}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	scope := ExternalPrevisScope{DramaID: "drama-1", EpisodeID: "episode-1", CanvasID: "canvas-1"}
	listed, err := s.ListExternalPrevisScenes("user", scope)
	if err != nil {
		t.Fatal(err)
	}
	create := ExternalPrevisCreateRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, CanvasSnapshotHash: listed["canvases"].([]map[string]any)[0]["canvasSnapshotHash"].(string), IdempotencyKey: "create-model-scene-01", Title: "宿舍", TemplateID: "empty"}
	scene, err := s.CreateExternalPrevisScene("user", create)
	if err != nil {
		t.Fatal(err)
	}
	resourceID := stored["resourceId"].(string)
	nodeIndex := 0
	name := "门"
	patch := ExternalPrevisPatchRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: scene["sceneId"].(string), SnapshotHash: scene["snapshotHash"].(string), IdempotencyKey: "place-model-node-01", Operations: []ExternalPrevisPatchOperation{{Type: "object_add", ID: "door", Name: &name, Kind: "model", ResourceID: &resourceID, GLTFNodeIndex: &nodeIndex}}}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err != nil {
		t.Fatal(err)
	}
	readScene, err := s.GetExternalPrevisScene("user", ExternalPrevisReadRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: patch.SceneID})
	if err != nil {
		t.Fatal(err)
	}
	objects := readScene["scene"].(map[string]any)["objects"].([]map[string]any)
	if len(objects) != 1 || objects[0]["resourceId"] != resourceID || objects[0]["gltfNodeIndex"] != float64(0) {
		t.Fatalf("model not readable: %#v", objects)
	}
	wrongIndex := 1
	patch.SnapshotHash = readScene["snapshotHash"].(string)
	patch.IdempotencyKey = "invalid-model-node-01"
	patch.Operations[0].GLTFNodeIndex = &wrongIndex
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err == nil {
		t.Fatal("non-mesh GLB node accepted")
	}
	data[0] = 'X'
	if _, err := externalPrevisParseGLB(data); err == nil {
		t.Fatal("invalid GLB accepted")
	}
}

func TestExternalPrevisCanvasAndShotStates(t *testing.T) {
	s, db, _, _ := creationTestService(t)
	for _, row := range []any{&model.Project{ID: "drama-1", UserID: "user", Name: "EP01", Type: "drama"}, &model.DramaProduction{ProjectID: "drama-1", UserID: "user"}, &model.DramaEpisode{ID: "episode-1", ProjectID: "drama-1", Number: 1}, &model.DramaEpisode{ID: "episode-2", ProjectID: "drama-1", Number: 2}, &model.DramaShot{ID: "drama-shot-1", ProjectID: "drama-1", EpisodeID: "episode-1", Position: 1}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	canvasReq := ExternalPrevisCanvasCreateRequest{DramaID: "drama-1", EpisodeID: "episode-1", Title: "EP01 预演", IdempotencyKey: "create-canvas-01"}
	created, err := s.CreateExternalPrevisCanvas("user", canvasReq)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.CreateExternalPrevisCanvas("user", canvasReq)
	if err != nil || replayed["canvasId"] != created["canvasId"] || replayed["created"] != false {
		t.Fatalf("canvas replay: %#v %v", replayed, err)
	}
	scope := ExternalPrevisScope{DramaID: "drama-1", EpisodeID: "episode-1", CanvasID: created["canvasId"].(string)}
	refs, err := s.ListExternalPrevisReferences("user", scope)
	if err != nil || len(refs["shots"].([]map[string]any)) != 1 {
		t.Fatalf("references: %#v %v", refs, err)
	}
	listed, err := s.ListExternalPrevisScenes("user", scope)
	if err != nil {
		t.Fatal(err)
	}
	createReq := ExternalPrevisCreateRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, CanvasSnapshotHash: listed["canvases"].([]map[string]any)[0]["canvasSnapshotHash"].(string), IdempotencyKey: "create-scene-01", Title: "水果店", TemplateID: "empty"}
	scene, err := s.CreateExternalPrevisScene("user", createReq)
	if err != nil {
		t.Fatal(err)
	}
	shotID := scene["shotId"].(string)
	name := "卷帘门"
	position := []float64{0, 1, 0}
	visible := false
	ratio := "9:16"
	dramaShotID := "drama-shot-1"
	objectID := "door"
	patch := ExternalPrevisPatchRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: scene["sceneId"].(string), SnapshotHash: scene["snapshotHash"].(string), IdempotencyKey: "door-shot-state-01", Operations: []ExternalPrevisPatchOperation{{Type: "object_add", ID: objectID, Name: &name, Kind: "primitive", Primitive: "box"}, {Type: "shot_update", ID: shotID, DramaShotID: &dramaShotID, AspectRatio: &ratio}, {Type: "shot_object_set", ID: shotID, ObjectID: &objectID, Visible: &visible, Position: &position}}}
	result, err := s.ApplyExternalPrevisPatch("user", patch)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.GetExternalPrevisScene("user", ExternalPrevisReadRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: patch.SceneID})
	if err != nil {
		t.Fatal(err)
	}
	shots := read["scene"].(map[string]any)["shots"].([]map[string]any)
	if len(shots) != 1 || shots[0]["dramaShotId"] != dramaShotID || shots[0]["aspectRatio"] != ratio || len(shots[0]["objectStates"].([]map[string]any)) != 1 {
		t.Fatalf("shot state missing: %#v", shots)
	}
	validation, err := s.ValidateExternalPrevisScene("user", ExternalPrevisReadRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: patch.SceneID})
	if err != nil || validation["valid"] != true {
		t.Fatalf("validation: %#v %v", validation, err)
	}
	previewUpload := ExternalPrevisRenderUploadRequest{FileName: "shot.png", DataBase64: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/mV8AAAAASUVORK5CYII=", Kind: "frame", IdempotencyKey: "shot-frame-upload-01"}
	preview, err := s.UploadExternalPrevisRender("user", previewUpload)
	if err != nil {
		t.Fatal(err)
	}
	publish, err := s.PublishExternalPrevisRender("user", ExternalPrevisRenderPublishRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: patch.SceneID, ShotID: shotID, SnapshotHash: result["snapshotHash"].(string), ResourceID: preview["resourceId"].(string), Kind: "frame", IdempotencyKey: "publish-frame-01"})
	if err != nil || publish["committed"] != true {
		t.Fatalf("publish preview: %#v %v", publish, err)
	}
	gotPreview, err := s.GetExternalPrevisRender("user", ExternalPrevisReadRequest{DramaID: scope.DramaID, EpisodeID: scope.EpisodeID, CanvasID: scope.CanvasID, SceneID: patch.SceneID, ShotID: shotID})
	if err != nil || gotPreview["resourceId"] != preview["resourceId"] {
		t.Fatalf("read preview: %#v %v", gotPreview, err)
	}
	bad := patch
	bad.SnapshotHash = result["snapshotHash"].(string)
	bad.IdempotencyKey = "cross-episode-shot-01"
	bad.Operations = []ExternalPrevisPatchOperation{{Type: "shot_update", ID: shotID, DramaShotID: &dramaShotID}}
	bad.EpisodeID = "episode-2"
	if _, err := s.ApplyExternalPrevisPatch("user", bad); err == nil {
		t.Fatal("cross-episode shot reference accepted")
	}
}

func TestExternalPrevisLegacySceneBindsOnFirstWrite(t *testing.T) {
	s, db, _, _ := creationTestService(t)
	for _, row := range []any{&model.Project{ID: "drama-1", UserID: "user", Name: "EP01", Type: "drama"}, &model.DramaProduction{ProjectID: "drama-1", UserID: "user"}, &model.DramaEpisode{ID: "episode-1", ProjectID: "drama-1", Number: 1}, &model.DramaEpisode{ID: "episode-2", ProjectID: "drama-1", Number: 2}, &model.CanvasProject{ID: "canvas-1", UserID: "user", ProjectID: "drama-1", Title: "预演", PayloadJSON: `{"nodes":[],"connections":[],"previsScenes":[]}`}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	canvas, doc, err := cloudAgentPrevisCanvas(s.repo, "user", "canvas-1")
	if err != nil {
		t.Fatal(err)
	}
	call, err := externalPrevisCall("previs_scene_create", cloudAgentPrevisSceneCreateArgs{CanvasSnapshotHash: cloudAgentCanvasHash(doc), SceneID: "old-scene", Title: "旧场景", TemplateID: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	created, err := applyCloudAgentPrevisMutation(s.repo, "user", canvas.ID, call, policy)
	if err != nil {
		t.Fatal(err)
	}
	current := created.(map[string]any)
	name := "桌子"
	patch := ExternalPrevisPatchRequest{DramaID: "drama-1", EpisodeID: "episode-1", CanvasID: "canvas-1", SceneID: "old-scene", SnapshotHash: current["snapshotHash"].(string), IdempotencyKey: "legacy-bind-01", Operations: []ExternalPrevisPatchOperation{{Type: "object_add", ID: "table", Name: &name, Kind: "primitive", Primitive: "box"}}}
	if _, err := s.ApplyExternalPrevisPatch("user", patch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetExternalPrevisScene("user", ExternalPrevisReadRequest{DramaID: "drama-1", EpisodeID: "episode-2", CanvasID: "canvas-1", SceneID: "old-scene"}); err == nil {
		t.Fatal("legacy scene remained writable across episodes")
	}
}
