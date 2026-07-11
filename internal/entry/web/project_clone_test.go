package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestCloneProjectCopiesFilesAndRewritesProjectPaths(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Source Novel")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	sourceUpload := filepath.Join(source.RootDir, "uploads", "adaptation", "source.txt")
	cloneTestWriteFile(t, sourceUpload, []byte("original novel text"))
	cloneTestWriteJSON(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_manifest.json"), map[string]any{
		"source_path": sourceUpload,
		"title":       "Source Novel",
	})
	cloneTestWriteJSON(t, filepath.Join(source.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath)), map[string]any{
		"version":     1,
		"kind":        "adapt",
		"source_file": "source.txt",
		"source_path": sourceUpload,
	})
	chapterPath := filepath.Join(source.OutputDir, "chapters", "chapter-001.md")
	cloneTestWriteFile(t, chapterPath, []byte("chapter body"))
	cloneTestWriteFile(t, filepath.Join(source.RootDir, "uploads", "custom.json"), []byte("not program-owned JSON"))
	cloneTestWriteFile(t, filepath.Join(source.RootDir, filepath.FromSlash(actionRegistryRelPath)), []byte(`{"project_id":"old"}`))
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "meta", "runtime", "queue.jsonl"), []byte("running task"))
	cloneTestWriteFile(t, filepath.Join(source.RootDir, ".tmp-clone-staging"), []byte("temporary"))
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, ".tmp-draft"), []byte("temporary"))

	sourceManifestBefore := cloneTestReadFile(t, filepath.Join(source.RootDir, "project.json"))
	sourceAdaptationBefore := cloneTestReadFile(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_manifest.json"))
	time.Sleep(2 * time.Millisecond)

	cloned, err := store.CloneProject(source.ID, "Source Novel - Copy")
	if err != nil {
		t.Fatalf("CloneProject: %v", err)
	}

	if cloned.ID == source.ID || cloned.ID == "" {
		t.Fatalf("cloned ID = %q, source ID = %q", cloned.ID, source.ID)
	}
	if cloned.Name != "Source Novel - Copy" {
		t.Fatalf("cloned name = %q", cloned.Name)
	}
	if filepath.Clean(cloned.RootDir) == filepath.Clean(source.RootDir) {
		t.Fatalf("clone shares source root %q", cloned.RootDir)
	}
	if filepath.Clean(cloned.OutputDir) == filepath.Clean(source.OutputDir) {
		t.Fatalf("clone shares source output dir %q", cloned.OutputDir)
	}
	if !cloned.CreatedAt.After(source.CreatedAt) || !cloned.UpdatedAt.After(source.UpdatedAt) || !cloned.LastAccessedAt.After(source.LastAccessedAt) {
		t.Fatalf("clone timestamps were not regenerated: source=%+v clone=%+v", source, cloned)
	}
	if cloned.DeletedAt != nil {
		t.Fatalf("cloned project is deleted: %+v", cloned)
	}

	clonedChapter := filepath.Join(cloned.OutputDir, "chapters", "chapter-001.md")
	if got := string(cloneTestReadFile(t, clonedChapter)); got != "chapter body" {
		t.Fatalf("cloned chapter = %q", got)
	}
	clonedUpload := filepath.Join(cloned.RootDir, "uploads", "adaptation", "source.txt")
	if got := string(cloneTestReadFile(t, clonedUpload)); got != "original novel text" {
		t.Fatalf("cloned upload = %q", got)
	}
	if got := string(cloneTestReadFile(t, filepath.Join(cloned.RootDir, "uploads", "custom.json"))); got != "not program-owned JSON" {
		t.Fatalf("cloned user JSON = %q", got)
	}
	cloneTestAssertSourcePath(t, filepath.Join(cloned.OutputDir, "meta", "adaptation", "source_manifest.json"), clonedUpload)
	cloneTestAssertSourcePath(t, filepath.Join(cloned.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath)), clonedUpload)

	for _, ignored := range []string{
		filepath.Join(cloned.RootDir, ".tmp-clone-staging"),
		filepath.Join(cloned.OutputDir, ".tmp-draft"),
		filepath.Join(cloned.RootDir, filepath.FromSlash(actionRegistryRelPath)),
		filepath.Join(cloned.OutputDir, "meta", "runtime", "queue.jsonl"),
	} {
		if _, err := os.Stat(ignored); !os.IsNotExist(err) {
			t.Fatalf("temporary file %q was cloned: %v", ignored, err)
		}
	}

	if after := cloneTestReadFile(t, filepath.Join(source.RootDir, "project.json")); !bytes.Equal(after, sourceManifestBefore) {
		t.Fatal("source project manifest changed during clone")
	}
	if after := cloneTestReadFile(t, filepath.Join(source.OutputDir, "meta", "adaptation", "source_manifest.json")); !bytes.Equal(after, sourceAdaptationBefore) {
		t.Fatal("source adaptation manifest changed during clone")
	}
	if got := string(cloneTestReadFile(t, chapterPath)); got != "chapter body" {
		t.Fatalf("source chapter changed to %q", got)
	}
}

func TestCloneProjectFailureLeavesNoPartialProject(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "runtime"))
	source, err := store.CreateProject("Broken Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source.RootDir, "project.json"), []byte("not-json"), 0o644); err != nil {
		t.Fatalf("corrupt source manifest: %v", err)
	}

	before := cloneTestProjectEntries(t, store.ProjectsDir())
	if _, err := store.CloneProject(source.ID, "Should Fail"); err == nil {
		t.Fatal("CloneProject succeeded with a corrupt source manifest")
	}
	after := cloneTestProjectEntries(t, store.ProjectsDir())
	if len(after) != len(before) {
		t.Fatalf("project directories after failed clone = %v, before = %v", after, before)
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("project directories after failed clone = %v, before = %v", after, before)
		}
	}
}

func TestCloneProjectHandlerCreatesIndependentProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	source, err := server.store.CreateProject("HTTP Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	cloneTestWriteFile(t, filepath.Join(source.OutputDir, "chapter.md"), []byte("http clone body"))

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+source.ID+"/clone", bytes.NewBufferString(`{"name":"HTTP Copy"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("clone status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Project         ProjectManifest `json:"project"`
		SourceProjectID string          `json:"source_project_id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode clone response: %v", err)
	}
	if response.Project.ID == "" || response.Project.ID == source.ID || response.Project.Name != "HTTP Copy" {
		t.Fatalf("clone response = %+v", response)
	}
	if response.SourceProjectID != source.ID {
		t.Fatalf("source_project_id = %q, want %q", response.SourceProjectID, source.ID)
	}
	if got := string(cloneTestReadFile(t, filepath.Join(response.Project.OutputDir, "chapter.md"))); got != "http clone body" {
		t.Fatalf("HTTP cloned chapter = %q", got)
	}
}

func TestCloneProjectHandlerRejectsRunningProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	source, err := server.store.CreateProject("Running Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, source)
	fake.snapshot = host.UISnapshot{IsRunning: true}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+source.ID+"/clone", bytes.NewBufferString(`{"name":"Rejected Copy"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("clone running project status = %d body=%s", rec.Code, rec.Body.String())
	}
	projects, err := server.store.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != source.ID {
		t.Fatalf("projects after rejected clone = %+v", projects)
	}
}

func cloneTestWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	cloneTestWriteFile(t, path, data)
}

func cloneTestWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func cloneTestReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func cloneTestAssertSourcePath(t *testing.T, path, want string) {
	t.Helper()
	var payload struct {
		SourcePath string `json:"source_path"`
	}
	if err := json.Unmarshal(cloneTestReadFile(t, path), &payload); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if filepath.Clean(payload.SourcePath) != filepath.Clean(want) {
		t.Fatalf("%s source_path = %q, want %q", path, payload.SourcePath, want)
	}
}

func cloneTestProjectEntries(t *testing.T, projectsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		t.Fatalf("read projects dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
