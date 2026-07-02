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
	"github.com/voocel/ainovel-cli/internal/domain"
)

type testMultipartFile struct {
	field    string
	filename string
	body     string
}

func TestProjectSimulateFilesUploadSavesSourcesUnderProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	manifest, err := server.store.CreateProject("Simulation Upload")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/files", []testMultipartFile{
		{field: "files", filename: "chapter-one.txt", body: "first source"},
		{field: "files", filename: "voice-notes.md", body: "# second source"},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"chapter-one.txt", "voice-notes.md"} {
		path := filepath.Join(manifest.RootDir, "simulate", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("uploaded file %s was not saved: %v", name, err)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			t.Fatalf("uploaded file %s is empty", name)
		}
	}

	var response struct {
		Files []apiUploadedFile `json:"files"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if len(response.Files) != 2 {
		t.Fatalf("files = %+v, want 2 uploaded files", response.Files)
	}
}

func TestProjectSimulateFilesRejectsUnsafeEmptyAndDuplicateNames(t *testing.T) {
	cases := []struct {
		name     string
		files    []testMultipartFile
		existing string
		status   int
		want     string
	}{
		{
			name:   "path traversal",
			files:  []testMultipartFile{{field: "files", filename: "../evil.txt", body: "source"}},
			status: http.StatusBadRequest,
			want:   "path separators",
		},
		{
			name:   "absolute path",
			files:  []testMultipartFile{{field: "files", filename: "C:\\temp\\evil.txt", body: "source"}},
			status: http.StatusBadRequest,
			want:   "absolute path",
		},
		{
			name:   "reserved name",
			files:  []testMultipartFile{{field: "files", filename: "CON.txt", body: "source"}},
			status: http.StatusBadRequest,
			want:   "reserved",
		},
		{
			name:   "unsupported extension",
			files:  []testMultipartFile{{field: "files", filename: "profile.json", body: "{}"}},
			status: http.StatusBadRequest,
			want:   "unsupported extension",
		},
		{
			name:   "empty file",
			files:  []testMultipartFile{{field: "files", filename: "empty.txt", body: "   \n\t"}},
			status: http.StatusBadRequest,
			want:   "empty",
		},
		{
			name:   "duplicate in request",
			files:  []testMultipartFile{{field: "files", filename: "same.txt", body: "one"}, {field: "files", filename: "same.txt", body: "two"}},
			status: http.StatusConflict,
			want:   "duplicate file name",
		},
		{
			name:     "duplicate existing",
			files:    []testMultipartFile{{field: "files", filename: "exists.md", body: "new"}},
			existing: "exists.md",
			status:   http.StatusConflict,
			want:     "already exists",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
			defer server.Close()
			manifest, err := server.store.CreateProject(c.name)
			if err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			if c.existing != "" {
				if err := os.WriteFile(filepath.Join(manifest.RootDir, "simulate", c.existing), []byte("old"), 0o644); err != nil {
					t.Fatalf("write existing: %v", err)
				}
			}
			req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/files", c.files)
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

func TestProjectSimulateAnalyzeUsesProjectSimulateDir(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Analyze Project Dir")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/analyze", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("analyze status = %d body=%s", rec.Code, rec.Body.String())
	}
	want := filepath.Join(manifest.RootDir, "simulate")
	if fake.simulateDir != want {
		t.Fatalf("simulate dir = %q, want project simulate dir %q", fake.simulateDir, want)
	}
	if strings.Contains(filepath.Clean(fake.simulateDir), filepath.Clean("D:\\ainovel\\simulate")) {
		t.Fatalf("simulate dir should not point at repository simulate: %q", fake.simulateDir)
	}
}

func TestProjectSimulateImportSavesJSONUnderImportedProfilesAndCallsHost(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Import Profile")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/import", []testMultipartFile{
		{field: "profile", filename: "profile.json", body: `{"version":1}`},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPath := filepath.Join(manifest.RootDir, "profiles", "imported", "profile.json")
	if fake.importPath != wantPath {
		t.Fatalf("import path = %q, want %q", fake.importPath, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("imported JSON was not saved: %v", err)
	}
}

func TestProjectSimulateImportUsesExistingHostImportBehavior(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Real Import Profile")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	data, err := domain.MarshalSimulationProfile(testWebSimulationProfile("imported.txt", "sha-imported"))
	if err != nil {
		t.Fatalf("MarshalSimulationProfile: %v", err)
	}

	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/import", []testMultipartFile{
		{field: "profile", filename: "valid-profile.json", body: string(data)},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", rec.Code, rec.Body.String())
	}
	saved, err := os.ReadFile(filepath.Join(manifest.OutputDir, "meta", "simulation_profile.json"))
	if err != nil {
		t.Fatalf("read saved simulation profile: %v", err)
	}
	if !strings.Contains(string(saved), "imported.txt") {
		t.Fatalf("saved simulation profile does not include imported source: %s", string(saved))
	}
}

func TestProjectSimulateImportRejectsBadJSON(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Bad Profile")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/import", []testMultipartFile{
		{field: "profile", filename: "bad.json", body: `{"version":`},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.importCalls != 0 {
		t.Fatalf("invalid JSON should not call import, calls=%d", fake.importCalls)
	}
	if _, err := os.Stat(filepath.Join(manifest.RootDir, "profiles", "imported", "bad.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid JSON should not be saved, stat err=%v", err)
	}
}

func installFakeSession(t *testing.T, server *Server, manifest ProjectManifest) *fakeProjectHost {
	t.Helper()
	fake := newFakeProjectHost()
	session, err := NewProjectSession(manifest, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	server.sessions.mu.Lock()
	server.sessions.sessions[manifest.ID] = session
	server.sessions.mu.Unlock()
	return fake
}

func newMultipartUploadRequest(t *testing.T, method, path string, files []testMultipartFile) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		field := file.field
		if field == "" {
			field = "files"
		}
		part, err := writer.CreateFormFile(field, file.filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write([]byte(file.body)); err != nil {
			t.Fatalf("write multipart part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func testWebSimulationProfile(path, sha string) domain.SimulationProfile {
	fingerprint := domain.SimulationSourceFingerprint(path, sha)
	return domain.SimulationProfile{
		Version: domain.SimulationProfileVersion,
		Corpus: domain.SimulationCorpusManifest{
			Sources: []domain.SimulationSource{{
				RelativePath: path,
				SHA256:       sha,
				Fingerprint:  fingerprint,
			}},
		},
		SourceReports: []domain.SimulationSourceReport{{
			RelativePath: path,
			SHA256:       sha,
			Fingerprint:  fingerprint,
			Summary:      "clear source summary",
		}},
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"close third person"},
			},
			RoleGuidance: domain.SimulationRoleGuidance{
				Writer: []string{"borrow pacing, not plot"},
			},
		},
	}
}
