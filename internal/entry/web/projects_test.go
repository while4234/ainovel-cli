package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
)

func TestProjectManifestCreateListOpenTouches(t *testing.T) {
	store := NewProjectStore(filepath.Join(t.TempDir(), "novels"))

	created, err := store.CreateProject("My Test Novel")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if created.ID == "" || created.Name != "My Test Novel" {
		t.Fatalf("created manifest mismatch: %+v", created)
	}
	for _, dir := range []string{
		created.RootDir,
		filepath.Join(created.RootDir, "simulate"),
		filepath.Join(created.RootDir, "uploads"),
		filepath.Join(created.RootDir, "uploads", "adaptation"),
		filepath.Join(created.RootDir, "profiles"),
		created.OutputDir,
	} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("expected project dir %s: info=%v err=%v", dir, info, err)
		}
	}
	if _, err := os.Stat(filepath.Join(created.RootDir, "project.json")); err != nil {
		t.Fatalf("project manifest not written: %v", err)
	}

	projects, err := store.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != created.ID {
		t.Fatalf("projects = %+v, want created project", projects)
	}

	time.Sleep(10 * time.Millisecond)
	opened, err := store.OpenProject(created.ID)
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	if !opened.LastAccessedAt.After(created.LastAccessedAt) {
		t.Fatalf("LastAccessedAt was not updated: before=%s after=%s", created.LastAccessedAt, opened.LastAccessedAt)
	}
}

func TestOpenProjectHostUsesProjectOutputDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(t.TempDir(), "novels"))
	manifest, err := store.CreateProject("Output Isolation")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	cfg := testWebConfig(t)

	h, err := store.OpenProjectHost(cfg, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	defer h.Close()
	if h.Dir() != manifest.OutputDir {
		t.Fatalf("host dir = %q, want project output %q", h.Dir(), manifest.OutputDir)
	}
	if info, err := os.Stat(filepath.Join(manifest.OutputDir, "meta")); err != nil || !info.IsDir() {
		t.Fatalf("host store did not initialize project output: info=%v err=%v", info, err)
	}
}
