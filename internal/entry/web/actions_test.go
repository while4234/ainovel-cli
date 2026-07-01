package web

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/exp"
)

func TestProjectStartQuickUsesPreparedStartupPrompt(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Quick Start")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/start", bytes.NewBufferString(`{"text":"写一个月城悬疑故事"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.prepareRulesCalls != 1 {
		t.Fatalf("PrepareUserRules calls = %d, want 1", fake.prepareRulesCalls)
	}
	if fake.startPreparedCalls != 1 {
		t.Fatalf("StartPrepared calls = %d, want 1", fake.startPreparedCalls)
	}
	if !strings.Contains(fake.preparedRulesPrompt, "写一个月城悬疑故事") {
		t.Fatalf("prepared rules prompt lost raw user prompt: %q", fake.preparedRulesPrompt)
	}
	if !strings.Contains(fake.startPreparedPrompt, "写一个月城悬疑故事") {
		t.Fatalf("start prompt lost user prompt: %q", fake.startPreparedPrompt)
	}
}

func TestProjectStartQuickPersistsTargetTotalWords(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Quick Budget")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/start", bytes.NewBufferString(`{"text":"写一部短篇小说","target_total_words":5000}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.setWordBudgetCalls != 1 || fake.wordBudget == nil || fake.wordBudget.TargetTotalWords != 5000 {
		t.Fatalf("SetWordBudget calls=%d budget=%+v", fake.setWordBudgetCalls, fake.wordBudget)
	}
	if !strings.Contains(fake.startPreparedPrompt, "target_total_words=5000") {
		t.Fatalf("start prompt missing target_total_words: %q", fake.startPreparedPrompt)
	}
}

func TestProjectStartQuickRejectsProjectWithExistingBookState(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Existing State")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{NovelName: "旧书", TotalChapters: 10}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/start", bytes.NewBufferString(`{"text":"新故事"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("start status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if fake.startPreparedCalls != 0 {
		t.Fatalf("existing book state should not call StartPrepared, calls=%d", fake.startPreparedCalls)
	}
}

func TestProjectPauseCallsAbort(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Pause")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/pause", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("pause status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.abortCalls != 1 {
		t.Fatalf("Abort calls = %d, want 1", fake.abortCalls)
	}
	if !strings.Contains(rec.Body.String(), `"stopped":true`) {
		t.Fatalf("pause response should report stopped=true: %s", rec.Body.String())
	}
}

func TestProjectImportSavesSourceUnderProjectAndResumes(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("External Import")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := newImportRequest(t, "/api/projects/"+manifest.ID+"/import", "source.txt", "第一章\n第二章", "2")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPath := filepath.Join(manifest.RootDir, "uploads", "import", "source.txt")
	if fake.importNovelPath != wantPath {
		t.Fatalf("import path = %q, want %q", fake.importNovelPath, wantPath)
	}
	if fake.importNovelResumeFrom != 2 {
		t.Fatalf("resumeFrom = %d, want 2", fake.importNovelResumeFrom)
	}
	if fake.resumeCalls != 1 {
		t.Fatalf("import should resume writing, calls=%d", fake.resumeCalls)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("import source was not saved: %v", err)
	}
}

func TestProjectExportUsesProjectExportsDir(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Export")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.exportResult = &exp.Result{Path: filepath.Join(manifest.RootDir, "exports", "book.epub"), Chapters: 3, Bytes: 1024, Skipped: []int{2}}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/export", bytes.NewBufferString(`{"path":"book.epub","format":"epub","from":1,"to":3,"overwrite":true}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPath := filepath.Join(manifest.RootDir, "exports", "book.epub")
	if fake.exportOptions.OutPath != wantPath {
		t.Fatalf("export path = %q, want %q", fake.exportOptions.OutPath, wantPath)
	}
	if fake.exportOptions.Format != exp.FormatEPUB || fake.exportOptions.From != 1 || fake.exportOptions.To != 3 || !fake.exportOptions.Overwrite {
		t.Fatalf("export options = %+v", fake.exportOptions)
	}
	var body struct {
		Export apiExportResult `json:"export"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if body.Export.Chapters != 3 || body.Export.Bytes != 1024 || len(body.Export.Skipped) != 1 {
		t.Fatalf("export result = %+v", body.Export)
	}
}

func TestProjectExportRejectsAbsolutePath(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Unsafe Export")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/export", bytes.NewBufferString(`{"path":"C:\\temp\\book.txt","format":"txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("export status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.exportCalls != 0 {
		t.Fatalf("unsafe export path should not call host, calls=%d", fake.exportCalls)
	}
}

func TestProjectDiagReturnsReportAndWritesExport(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Diagnostics")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/diag", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("diag status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Project    ProjectManifest `json:"project"`
		Report     map[string]any  `json:"report"`
		ExportPath string          `json:"export_path"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode diag: %v", err)
	}
	if body.Project.ID != manifest.ID || body.Report == nil {
		t.Fatalf("diag response missing project/report: %+v", body)
	}
	if body.ExportPath != "" {
		if _, err := os.Stat(body.ExportPath); err != nil {
			t.Fatalf("diag export was not written: %v", err)
		}
	}
}

func newImportRequest(t *testing.T, path, filename, body, from string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if from != "" {
		if err := writer.WriteField("from", from); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	part, err := writer.CreateFormFile("source", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatalf("write multipart part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}
