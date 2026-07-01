package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestProjectAdaptSourceUploadSavesSourceUnderProjectUploads(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()

	manifest, err := server.store.CreateProject("Adapt Upload")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/source", []testMultipartFile{
		{field: "source", filename: "source-novel.txt", body: "第1章 开始\n原文内容"},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source-novel.txt")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("uploaded adaptation source was not saved under project uploads: %v", err)
	}
	if _, err := os.Stat(filepath.Join(manifest.RootDir, "simulate", "source-novel.txt")); !os.IsNotExist(err) {
		t.Fatalf("adaptation source must not be saved under simulate, stat err=%v", err)
	}

	var response struct {
		SourceFile apiUploadedFile `json:"source_file"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if response.SourceFile.RelativePath != "source-novel.txt" {
		t.Fatalf("source relative path = %q, want source-novel.txt", response.SourceFile.RelativePath)
	}
}

func TestProjectAdaptSourceRejectsUnsafeAndMultipleFiles(t *testing.T) {
	cases := []struct {
		name   string
		files  []testMultipartFile
		status int
		want   string
	}{
		{
			name:   "path traversal",
			files:  []testMultipartFile{{field: "source", filename: "../source.txt", body: "source"}},
			status: http.StatusBadRequest,
			want:   "path separators",
		},
		{
			name:   "absolute path",
			files:  []testMultipartFile{{field: "source", filename: "C:\\temp\\source.txt", body: "source"}},
			status: http.StatusBadRequest,
			want:   "absolute path",
		},
		{
			name:   "unsupported extension",
			files:  []testMultipartFile{{field: "source", filename: "source.json", body: "{}"}},
			status: http.StatusBadRequest,
			want:   "unsupported extension",
		},
		{
			name: "multiple files",
			files: []testMultipartFile{
				{field: "source", filename: "one.txt", body: "one"},
				{field: "source", filename: "two.txt", body: "two"},
			},
			status: http.StatusBadRequest,
			want:   "exactly one",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
			defer server.Close()
			manifest, err := server.store.CreateProject(c.name)
			if err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/source", c.files)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)

			if rec.Code != c.status {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), c.status)
			}
			if !strings.Contains(rec.Body.String(), c.want) {
				t.Fatalf("body %q does not contain %q", rec.Body.String(), c.want)
			}
		})
	}
}

func TestProjectAdaptAnalyzeUsesProjectAdaptationUploadPath(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt Analyze")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	sourceDir := filepath.Join(manifest.RootDir, "uploads", "adaptation")
	if err := os.WriteFile(filepath.Join(sourceDir, "source.txt"), []byte("第1章 开始\n内容"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/analyze", bytes.NewBufferString(`{"source_file":"source.txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("analyze status = %d body=%s", rec.Code, rec.Body.String())
	}
	want := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source.txt")
	if fake.adaptSourcePath != want {
		t.Fatalf("adapt source path = %q, want %q", fake.adaptSourcePath, want)
	}
	if strings.Contains(filepath.Clean(fake.adaptSourcePath), filepath.Clean("D:\\ainovel\\uploads\\adaptation")) {
		t.Fatalf("adapt source path should not point at repository uploads: %q", fake.adaptSourcePath)
	}
}

func TestProjectAdaptAnalyzeRejectsUnsafeSourceFile(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Unsafe Analyze")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/analyze", bytes.NewBufferString(`{"source_file":"../evil.txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("analyze status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.adaptAnalyzeCalls != 0 {
		t.Fatalf("unsafe source file should not call host, calls=%d", fake.adaptAnalyzeCalls)
	}
}

func TestProjectAdaptStartStrictModesMapRewritePolicy(t *testing.T) {
	cases := []struct {
		mode       string
		wantPolicy string
	}{
		{mode: domain.AdaptationGranularityChapter, wantPolicy: domain.AdaptationRewritePreserveDetails},
		{mode: domain.AdaptationGranularityArc, wantPolicy: domain.AdaptationRewriteFullRewrite},
		{mode: domain.AdaptationGranularityFree, wantPolicy: domain.AdaptationRewriteFullRewrite},
	}

	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
			defer server.Close()
			manifest, err := server.store.CreateProject("Adapt Start " + c.mode)
			if err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			fake := installFakeSession(t, server, manifest)
			writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")

			body := `{"mode":` + strconvQuote(c.mode) + `,"brief":"改成现代悬疑，保留主线"}`
			body = `{"source_file":"source.txt","mode":` + strconvQuote(c.mode) + `,"brief":"adapt this source"}`
			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/start", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
			}
			if fake.adaptStartCalls != 1 {
				t.Fatalf("adapt start calls = %d, want 1", fake.adaptStartCalls)
			}
			if fake.adaptOptions.Granularity != c.mode || fake.adaptOptions.RewritePolicy != c.wantPolicy {
				t.Fatalf("adapt options = %+v, want mode %s policy %s", fake.adaptOptions, c.mode, c.wantPolicy)
			}
			wantSourcePath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source.txt")
			if fake.adaptOptions.SourcePath != wantSourcePath {
				t.Fatalf("adapt source path = %q, want %q", fake.adaptOptions.SourcePath, wantSourcePath)
			}
		})
	}
}

func TestProjectAdaptStartFailsAfterNewUploadUntilAnalyzeCompletes(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Stale Adapt Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.requireAnalyzedAdaptSource = true

	uploadAdaptationSourceForTest(t, server, manifest, "old.txt", "Chapter 1\nold source")
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/analyze", bytes.NewBufferString(`{"source_file":"old.txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("old analyze status = %d body=%s", rec.Code, rec.Body.String())
	}

	uploadAdaptationSourceForTest(t, server, manifest, "new.txt", "Chapter 1\nnew source")
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/start", bytes.NewBufferString(`{"source_file":"new.txt","mode":"chapter","brief":"adapt the new source"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale start status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "has not completed analysis") {
		t.Fatalf("stale start body %q does not explain analysis requirement", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/analyze", bytes.NewBufferString(`{"source_file":"new.txt"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new analyze status = %d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/start", bytes.NewBufferString(`{"source_file":"new.txt","mode":"chapter","brief":"adapt the new source"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh start status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if fake.adaptStartCalls != 2 {
		t.Fatalf("adapt start calls = %d, want stale attempt plus fresh attempt", fake.adaptStartCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/continue", bytes.NewBufferString(`{"text":"ordinary path still works"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ordinary continue status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if fake.continueCalls != 1 {
		t.Fatalf("continue calls = %d, want 1", fake.continueCalls)
	}
}

func TestProjectAdaptStartRequiresSourceFile(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Missing Adapt Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/start", bytes.NewBufferString(`{"mode":"chapter","brief":"adapt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("start status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.adaptStartCalls != 0 {
		t.Fatalf("missing source_file should not call host, calls=%d", fake.adaptStartCalls)
	}
}

func TestProjectAdaptStartRejectsUnsupportedMode(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Bad Adapt Mode")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/start", bytes.NewBufferString(`{"mode":"summary","brief":"改编"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("start status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chapter, arc, free") {
		t.Fatalf("body %q does not explain strict modes", rec.Body.String())
	}
	if fake.adaptStartCalls != 0 {
		t.Fatalf("unsupported mode should not call host, calls=%d", fake.adaptStartCalls)
	}
}

func uploadAdaptationSourceForTest(t *testing.T, server *Server, manifest ProjectManifest, filename, body string) {
	t.Helper()
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/source", []testMultipartFile{
		{field: "source", filename: filename, body: body},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload %s status = %d body=%s", filename, rec.Code, rec.Body.String())
	}
}

func writeAdaptationUpload(t *testing.T, manifest ProjectManifest, filename, body string) {
	t.Helper()
	sourceDir := filepath.Join(manifest.RootDir, "uploads", "adaptation")
	if err := os.WriteFile(filepath.Join(sourceDir, filename), []byte(body), 0o644); err != nil {
		t.Fatalf("write adaptation upload %s: %v", filename, err)
	}
}
