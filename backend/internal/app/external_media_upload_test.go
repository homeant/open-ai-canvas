package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"yingce/backend/internal/model"
)

func externalUploadTestFile(t *testing.T, s *Service, name, kind string, data []byte) string {
	t.Helper()
	req := ExternalUploadBeginRequest{FileName: name, Kind: kind, Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), IdempotencyKey: name}
	begin, err := s.BeginExternalMediaUpload("user", req)
	if err != nil {
		t.Fatal(err)
	}
	id := begin["uploadId"].(string)
	if _, err = s.BeginExternalMediaUpload("other", req); err == nil {
		t.Fatal("unknown user accepted")
	}
	if _, err = s.ExternalMediaUploadStatus("other", id); err == nil {
		t.Fatal("cross-account upload visible")
	}
	req.Size++
	if _, err = s.BeginExternalMediaUpload("user", req); err == nil {
		t.Fatal("changed retry accepted")
	}
	if _, err = s.CompleteExternalMediaUpload("user", id); err == nil {
		t.Fatal("missing chunks accepted")
	}
	for i, start := 0, 0; start < len(data); i, start = i+1, start+externalUploadChunkSize {
		end := min(start+externalUploadChunkSize, len(data))
		chunk := data[start:end]
		in := ExternalUploadChunkRequest{UploadID: id, Index: i, DataBase64: base64.StdEncoding.EncodeToString(chunk), SHA256: fmt.Sprintf("%x", sha256.Sum256(chunk))}
		if _, err = s.PutExternalMediaUploadChunk("user", in); err != nil {
			t.Fatal(err)
		}
		if _, err = s.PutExternalMediaUploadChunk("user", in); err != nil {
			t.Fatal("identical chunk retry", err)
		}
		if i == 0 {
			bad := append([]byte{}, chunk...)
			bad[0] ^= 1
			in.DataBase64 = base64.StdEncoding.EncodeToString(bad)
			in.SHA256 = fmt.Sprintf("%x", sha256.Sum256(bad))
			if _, err = s.PutExternalMediaUploadChunk("user", in); err == nil {
				t.Fatal("different chunk accepted")
			}
		}
	}
	result, err := s.CompleteExternalMediaUpload("user", id)
	if err != nil {
		t.Fatal(err)
	}
	resource := result["resource"].(*model.Resource)
	if resource.Status != "ready" || resource.Size != int64(len(data)) || resource.UserID != "user" || result["adopted"] != false {
		t.Fatalf("bad result %+v", resource)
	}
	// Completion retry after a service restart uses the persisted manifest.
	restarted := &Service{repo: s.repo, dataDir: s.dataDir}
	again, err := restarted.CompleteExternalMediaUpload("user", id)
	if err != nil || again["resource"].(*model.Resource).ID != resource.ID {
		t.Fatalf("completion replay %v", err)
	}
	if _, err = s.AbortExternalMediaUpload("user", id); err == nil {
		t.Fatal("completed resource deleted")
	}
	return resource.ID
}

func TestExternalMediaUploadPNGAndGuards(t *testing.T) {
	s, db, _, _ := creationTestService(t)
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 32, 48))); err != nil {
		t.Fatal(err)
	}
	id := externalUploadTestFile(t, s, "fixture.png", "image", buf.Bytes())
	r, _ := s.repo.ResourceForUser("user", id)
	if r.Width != 32 || r.Height != 48 {
		t.Fatalf("dimensions %+v", r)
	}
	var count int64
	db.Model(&model.Resource{}).Where("user_id = ?", "user").Count(&count)
	if count != 1 {
		t.Fatalf("duplicate resources %d", count)
	}
	db.Model(&model.CanvasProject{}).Count(&count)
	if count != 0 {
		t.Fatal("upload created a canvas")
	}
	begin, err := s.BeginExternalMediaUpload("user", ExternalUploadBeginRequest{FileName: "abort.png", Kind: "image", Size: 1, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte{1})), IdempotencyKey: "abort"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AbortExternalMediaUpload("user", begin["uploadId"].(string)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutExternalMediaUploadChunk("user", ExternalUploadChunkRequest{UploadID: "../../outside"}); err == nil {
		t.Fatal("unsafe ID accepted")
	}
}

func TestExternalMediaUploadLargeMP4AndWAV(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if !mediaProbeAvailable() {
		t.Skip("ffprobe unavailable")
	}
	s, _, _, _ := creationTestService(t)
	file := filepath.Join(t.TempDir(), "large.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=180x320:rate=5", "-t", "1", "-c:v", "mpeg4", "-y", file).CombinedOutput(); err != nil {
		t.Fatalf("fixture %v %s", err, out)
	}
	// A valid ISO BMFF free box enlarges the fixture without changing its stream.
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	padding := 25 << 20
	header := []byte{byte(padding >> 24), byte(padding >> 16), byte(padding >> 8), byte(padding), 'f', 'r', 'e', 'e'}
	if _, err = f.Write(header); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(make([]byte, padding-8)); err != nil {
		t.Fatal(err)
	}
	f.Close()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	id := externalUploadTestFile(t, s, "large.mp4", "video", data)
	r, _ := s.repo.ResourceForUser("user", id)
	if r.DurationMs != 1000 || r.Width != 180 || r.Height != 320 {
		t.Fatalf("unmeasured %+v", r)
	}
	file = filepath.Join(t.TempDir(), "tone.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000", "-t", "1", "-y", file).CombinedOutput(); err != nil {
		t.Fatalf("wav %v %s", err, out)
	}
	data, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	id = externalUploadTestFile(t, s, "tone.wav", "audio", data)
	r, _ = s.repo.ResourceForUser("user", id)
	if r.DurationMs != 1000 {
		t.Fatalf("unmeasured audio %+v", r)
	}
}
