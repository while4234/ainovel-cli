package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestLegacyMigrationCopiesOutputSanitizesConfigAndIsIdempotent(t *testing.T) {
	t.Parallel()
	base := testTempDir(t)
	runtimeRoot := filepath.Join(base, "runtime")
	source := filepath.Join(base, "old-output")
	writeLegacyFixture(t, source)
	writeTestFile(t, filepath.Join(source, ".ainovel", "config.json"), `{
  "provider":"private-provider",
  "model":"writer-v1",
  "style":"fantasy",
  "providers":{"private-provider":{"label":"Private","api_key":"do-not-copy","base_url":"https://user:pass@example.test/v1","models":["writer-v1"]}},
  "roles":{"writer":{"provider":"private-provider","model":"writer-v1","reasoning_effort":"high"}},
  "notify":{"command":"run-secret-helper"},
  "proxy":"https://proxy-user:proxy-pass@example.test"
}`)

	server := NewServer(bootstrap.Config{}, assets.Bundle{}, runtimeRoot)
	defer server.Close()
	first := performLegacyMigration(t, server, source, "Imported Novel")
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	var created legacyMigrationResult
	decodeRecorderJSON(t, first, &created)
	if !created.Created || created.Project.Name != "Imported Novel" || created.SourceHash == "" {
		t.Fatalf("created result = %+v", created)
	}
	for relative, want := range map[string]string{
		"chapters/01.md":               "第一章\n正文",
		"outline.json":                 `[{"chapter":1}]`,
		"meta/checkpoints.jsonl":       `{"seq":1}`,
		"meta/sessions/main.jsonl":     `{"role":"coordinator"}`,
		"meta/adaptation/plan.json":    `{"status":"confirmed"}`,
		"meta/continuation/state.json": `{"status":"approved"}`,
		"meta/usage.json":              `{"version":2,"total":{"input_tokens":10}}`,
	} {
		data, err := os.ReadFile(filepath.Join(created.Project.OutputDir, filepath.FromSlash(relative)))
		if err != nil || string(data) != want {
			t.Fatalf("copied %s = %q, err=%v, want %q", relative, data, err, want)
		}
	}
	configData, err := os.ReadFile(ProjectConfigPath(created.Project))
	if err != nil {
		t.Fatalf("read sanitized config: %v", err)
	}
	configText := string(configData)
	for _, secret := range []string{"do-not-copy", "user:pass", "proxy-pass", "run-secret-helper", "api_key", "base_url"} {
		if strings.Contains(configText, secret) {
			t.Fatalf("sanitized config leaks %q: %s", secret, configText)
		}
	}
	safeConfig, err := bootstrap.LoadConfigFile(ProjectConfigPath(created.Project))
	if err != nil {
		t.Fatalf("parse sanitized config: %v", err)
	}
	if safeConfig.Provider != "private-provider" || safeConfig.ModelName != "writer-v1" || safeConfig.Style != "fantasy" {
		t.Fatalf("safe config fields not preserved: %+v", safeConfig)
	}
	if provider := safeConfig.Providers["private-provider"]; provider.Label != "Private" || len(provider.Models) != 1 || provider.APIKey != "" || provider.BaseURL != "" {
		t.Fatalf("provider was not sanitized: %+v", provider)
	}
	markerData, err := os.ReadFile(filepath.Join(created.Project.RootDir, filepath.FromSlash(legacyImportMarkerPath)))
	if err != nil || !bytes.Contains(markerData, []byte(created.SourceHash)) {
		t.Fatalf("migration marker missing hash: %s err=%v", markerData, err)
	}

	second := performLegacyMigration(t, server, source, "A Different Name")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	var duplicate legacyMigrationResult
	decodeRecorderJSON(t, second, &duplicate)
	if duplicate.Created || duplicate.Project.ID != created.Project.ID || duplicate.SourceHash != created.SourceHash {
		t.Fatalf("idempotent result = %+v, first = %+v", duplicate, created)
	}
	projects, err := server.store.ListProjects()
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects after retry = %d, err=%v", len(projects), err)
	}
}

func TestLegacyMigrationNeverOverwritesExistingProject(t *testing.T) {
	t.Parallel()
	base := testTempDir(t)
	server := NewServer(bootstrap.Config{}, assets.Bundle{}, filepath.Join(base, "runtime"))
	defer server.Close()
	existing, err := server.store.CreateProject("Same Name")
	if err != nil {
		t.Fatalf("create existing project: %v", err)
	}
	existingPath := filepath.Join(existing.OutputDir, "chapters", "01.md")
	writeTestFile(t, existingPath, "existing")
	source := filepath.Join(base, "legacy")
	writeLegacyFixture(t, source)

	response := performLegacyMigration(t, server, source, "Same Name")
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var imported legacyMigrationResult
	decodeRecorderJSON(t, response, &imported)
	if imported.Project.ID == existing.ID || imported.Project.RootDir == existing.RootDir {
		t.Fatalf("migration reused existing project: existing=%+v imported=%+v", existing, imported.Project)
	}
	data, err := os.ReadFile(existingPath)
	if err != nil || string(data) != "existing" {
		t.Fatalf("existing project was changed: %q err=%v", data, err)
	}
}

func TestLegacyMigrationRejectsUnsafeSources(t *testing.T) {
	t.Parallel()
	base := testTempDir(t)
	runtimeRoot := filepath.Join(base, "runtime")
	server := NewServer(bootstrap.Config{}, assets.Bundle{}, runtimeRoot)
	defer server.Close()

	t.Run("missing explicit directory", func(t *testing.T) {
		response := performLegacyMigration(t, server, "", "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("unrecognized directory", func(t *testing.T) {
		source := filepath.Join(base, "unrecognized")
		writeTestFile(t, filepath.Join(source, "random.txt"), "not a novel output")
		response := performLegacyMigration(t, server, source, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("runtime overlap", func(t *testing.T) {
		source := filepath.Join(runtimeRoot, "old-output")
		writeLegacyFixture(t, source)
		response := performLegacyMigration(t, server, source, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("symbolic link in source", func(t *testing.T) {
		source := filepath.Join(base, "linked-output")
		writeLegacyFixture(t, source)
		external := filepath.Join(base, "outside.txt")
		writeTestFile(t, external, "outside")
		if err := os.Symlink(external, filepath.Join(source, "chapters", "02.md")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		response := performLegacyMigration(t, server, source, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestLegacyMigrationRevalidatesFilesBeforeCopy(t *testing.T) {
	t.Parallel()
	base := testTempDir(t)
	source := filepath.Join(base, "legacy")
	writeLegacyFixture(t, source)
	store := NewProjectStore(filepath.Join(base, "runtime"))
	plan, err := store.buildLegacyImportPlan(source)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	chapterPath := filepath.Join(source, "chapters", "01.md")
	if err := os.Remove(chapterPath); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(base, "outside.md")
	writeTestFile(t, external, "outside")
	if err := os.Symlink(external, chapterPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, _, err = copyLegacyImportPlan(plan, filepath.Join(base, "destination"))
	if !errors.Is(err, errLegacySourceInvalid) {
		t.Fatalf("copy error = %v, want invalid source", err)
	}
}

func writeLegacyFixture(t *testing.T, source string) {
	t.Helper()
	files := map[string]string{
		"chapters/01.md":               "第一章\n正文",
		"outline.json":                 `[{"chapter":1}]`,
		"meta/checkpoints.jsonl":       `{"seq":1}`,
		"meta/sessions/main.jsonl":     `{"role":"coordinator"}`,
		"meta/adaptation/plan.json":    `{"status":"confirmed"}`,
		"meta/continuation/state.json": `{"status":"approved"}`,
		"meta/usage.json":              `{"version":2,"total":{"input_tokens":10}}`,
	}
	for relative, body := range files {
		writeTestFile(t, filepath.Join(source, filepath.FromSlash(relative)), body)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func performLegacyMigration(t *testing.T, server *Server, source, name string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(legacyMigrationRequest{SourceDir: source, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/projects/migrate-legacy", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	return recorder
}

func decodeRecorderJSON(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %s: %v", recorder.Body.String(), err)
	}
}
