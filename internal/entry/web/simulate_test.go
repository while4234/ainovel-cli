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
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
)

type testMultipartFile struct {
	field    string
	filename string
	body     string
}

const formerTextUploadLimit = 10 << 20

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

func TestProjectSnapshotRestoresUploadedSimulationSources(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	manifest, err := server.store.CreateProject("Simulation Snapshot")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/files", []testMultipartFile{
		{field: "files", filename: "b-source.md", body: "# second source"},
		{field: "files", filename: "a-source.txt", body: "first source"},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}

	var response projectSnapshotResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if got := len(response.Simulation.Files); got != 2 {
		t.Fatalf("simulation files = %+v, want 2 files", response.Simulation.Files)
	}
	if response.Simulation.Files[0].Name != "a-source.txt" || response.Simulation.Files[1].Name != "b-source.md" {
		t.Fatalf("simulation files not restored in stable order: %+v", response.Simulation.Files)
	}
	if response.Simulation.Files[0].Size <= 0 || response.Simulation.Files[1].Size <= 0 {
		t.Fatalf("simulation file sizes not restored: %+v", response.Simulation.Files)
	}
}

func TestProjectSimulateFilesUploadAllowsSourceLargerThanFormerTenMiBLimit(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Large Simulation Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	body := textBodyLargerThanFormerLimit()
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/files", []testMultipartFile{
		{field: "files", filename: "long-source.txt", body: body},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	info, err := os.Stat(filepath.Join(manifest.RootDir, "simulate", "long-source.txt"))
	if err != nil {
		t.Fatalf("stat uploaded source: %v", err)
	}
	if info.Size() <= formerTextUploadLimit {
		t.Fatalf("uploaded source size = %d, want greater than old limit %d", info.Size(), formerTextUploadLimit)
	}
}

func TestProjectSimulateFilesUploadSplitsLongChapteredSource(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Split Long Simulation Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/files", []testMultipartFile{
		{field: "files", filename: "novel.txt", body: longChapteredSimulationSource()},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(manifest.RootDir, "simulate", "novel.txt")); !os.IsNotExist(err) {
		t.Fatalf("original long source should be replaced by split parts, stat err=%v", err)
	}
	files, err := projectSimulationSourceFiles(filepath.Join(manifest.RootDir, "simulate"))
	if err != nil {
		t.Fatalf("projectSimulationSourceFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %+v, want 2 split parts", files)
	}
	if files[0].Name != "novel.part_001_ch0001-0002.txt" || files[1].Name != "novel.part_002_ch0003-0003.txt" {
		t.Fatalf("split files not in expected order: %+v", files)
	}
}

func TestProjectSimulateAnalyzeSplitsExistingLongSourceBeforeHostRun(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Analyze Split Long Simulation Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	simulateDir := filepath.Join(manifest.RootDir, "simulate")
	if err := os.MkdirAll(simulateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(simulateDir, "novel.txt"), []byte(longChapteredSimulationSource()), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/analyze", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("analyze status = %d body=%s", rec.Code, rec.Body.String())
	}
	waitForTestCondition(t, "simulation host call", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.simulateDir == simulateDir
	})
	if _, err := os.Stat(filepath.Join(simulateDir, "novel.txt")); !os.IsNotExist(err) {
		t.Fatalf("original long source should be replaced before analyze, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(simulateDir, "novel.part_001_ch0001-0002.txt")); err != nil {
		t.Fatalf("first split part missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(simulateDir, "novel.part_002_ch0003-0003.txt")); err != nil {
		t.Fatalf("second split part missing: %v", err)
	}
}

func TestMakeSimulationChapterPartsKeepsOversizeChapterAlone(t *testing.T) {
	chapters := []simulationChapterRef{
		{Number: 1, Title: "short", Body: "a", Runes: 10},
		{Number: 2, Title: "oversize", Body: strings.Repeat("b", simulationAutoSplitTargetRunes+1), Runes: simulationAutoSplitTargetRunes + 1},
		{Number: 3, Title: "tail", Body: "c", Runes: 10},
	}

	parts := makeSimulationChapterParts(chapters, simulationAutoSplitTargetRunes)

	if len(parts) != 3 {
		t.Fatalf("parts = %+v, want 3", parts)
	}
	if len(parts[1].Chapters) != 1 || parts[1].Chapters[0].Number != 2 {
		t.Fatalf("oversize chapter should be isolated in one part: %+v", parts[1])
	}
}

func TestValidateSimulationSplitQualityRejectsVeryLongNovelWithFewChapters(t *testing.T) {
	chapters := []simulationChapterRef{
		{Number: 1, Title: "one", Runes: 180000},
		{Number: 2, Title: "two", Runes: 180000},
		{Number: 3, Title: "three", Runes: 180000},
	}

	err := validateSimulationSplitQuality(chapters)

	if err == nil || !strings.Contains(err.Error(), "recognized only 3 chapters") {
		t.Fatalf("error = %v, want suspicious few-chapter error", err)
	}
}

func TestValidateSimulationSplitQualityRejectsHugeChapterOutlier(t *testing.T) {
	chapters := []simulationChapterRef{
		{Number: 1, Title: "one", Runes: 8000},
		{Number: 2, Title: "two", Runes: 7600},
		{Number: 3, Title: "three", Runes: 8200},
		{Number: 4, Title: "missed boundaries", Runes: 90000},
		{Number: 5, Title: "five", Runes: 7900},
	}

	err := validateSimulationSplitQuality(chapters)

	if err == nil || !strings.Contains(err.Error(), "chapter 4") {
		t.Fatalf("error = %v, want suspicious outlier error", err)
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
	waitForTestCondition(t, "simulation host call", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.simulateDir == want
	})
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
	waitForTestCondition(t, "simulation import host call", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.importPath == wantPath
	})
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
	profilePath := filepath.Join(manifest.OutputDir, "meta", "simulation_profile.json")
	waitForTestCondition(t, "saved simulation profile", func() bool {
		saved, err := os.ReadFile(profilePath)
		return err == nil && strings.Contains(string(saved), "imported.txt")
	})
	saved, err := os.ReadFile(profilePath)
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

func textBodyLargerThanFormerLimit() string {
	return strings.Repeat("x", formerTextUploadLimit+1)
}

func longChapteredSimulationSource() string {
	return strings.Join([]string{
		"Chapter 1 First",
		strings.Repeat("a", 7000),
		"Chapter 2 Second",
		strings.Repeat("b", 7000),
		"Chapter 3 Third",
		strings.Repeat("c", 3000),
	}, "\n\n")
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

func waitForTestCondition(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}
