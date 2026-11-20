package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func startupRequestPackage(t *testing.T, version, path string) []byte {
	t.Helper()
	return testPluginPackage(t, []byte(fmt.Sprintf(`{"apiVersion":"yingce.plugin/v2","id":"startup-video","version":%q,"name":"Startup video","author":"Test","documentation":"# Test","contributes":{"providers":[{"id":"startup-video","label":"Video","capabilities":["video"],"scopes":["canvas"],"create":{"method":"POST","path":%q,"body":{"prompt":{"$ref":"request.prompt"}}},"response":{"taskIdPaths":["id"],"statusPaths":["status"]}}]}}`, version, path)))
}

func TestOfficialRequestRepackPreservesInstalledReleaseAndAllowsVersionedUpgrade(t *testing.T) {
	dir, data := t.TempDir(), t.TempDir()
	t.Setenv("CANVAS_OFFICIAL_PLUGIN_DIR", dir)
	path := filepath.Join(dir, "video.yingce-plugin")
	write := func(version, endpoint string) []byte {
		t.Helper()
		archive := startupRequestPackage(t, version, endpoint)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, archive, 0o600); err != nil {
			t.Fatal(err)
		}
		return archive
	}
	original := write("1.0.0", "/original/jobs")
	first, err := newPluginRuntime(data)
	if err != nil {
		t.Fatal(err)
	}
	before := first.plugins["startup-video"]
	write("1.0.0", "/unversioned-replacement/jobs")
	for range 2 {
		restarted, err := newPluginRuntime(data)
		if err != nil {
			t.Fatalf("repacked release took startup offline: %v", err)
		}
		current := restarted.plugins["startup-video"]
		if current.PackageSHA256 != pluginHash(original) || current.PackagePath != before.PackagePath || string(current.Raw) != string(before.Raw) || current.Status != "enabled" || len(current.History) != 0 {
			t.Fatalf("installed protocol changed: %+v", current)
		}
	}
	upgraded := write("1.0.1", "/versioned/jobs")
	restarted, err := newPluginRuntime(data)
	if err != nil {
		t.Fatal(err)
	}
	current := restarted.plugins["startup-video"]
	if current.PackageSHA256 != pluginHash(upgraded) || len(current.History) != 1 || current.History[0].SHA256 != before.PackageSHA256 || current.History[0].Version != "1.0.0" {
		t.Fatalf("versioned upgrade lost admitted release: %+v", current)
	}
}

func TestOfficialRequestRepackDoesNotTrustCorruptInstalledArchive(t *testing.T) {
	dir, data := t.TempDir(), t.TempDir()
	t.Setenv("CANVAS_OFFICIAL_PLUGIN_DIR", dir)
	path := filepath.Join(dir, "video.yingce-plugin")
	if err := os.WriteFile(path, startupRequestPackage(t, "1.0.0", "/original/jobs"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := newPluginRuntime(data)
	if err != nil {
		t.Fatal(err)
	}
	old := runtime.plugins["startup-video"]
	// Replace the source inode: cached packages may be hard-linked to it.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, startupRequestPackage(t, "1.0.0", "/replacement/jobs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime.packageDir, old.PackagePath), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newPluginRuntime(data); err == nil || !strings.Contains(err.Error(), "完整性校验失败") {
		t.Fatalf("corrupt installed package accepted: %v", err)
	}
}
