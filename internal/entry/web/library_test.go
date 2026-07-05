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
	adaptengine "github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSimulationLibraryUploadSearchAndLoad(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	profile := testWebSimulationProfile("voice.txt", "abc123")
	profileData, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/libraries/simulation/upload", []testMultipartFile{
		{field: "files", filename: "voice.json", body: string(profileData)},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, simulationLibraryDirName, "voice.json")); err != nil {
		t.Fatalf("simulation profile was not saved to library: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/libraries/simulation?q=voi", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []apiLibraryItem `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "voice" {
		t.Fatalf("search items = %+v, want voice", list.Items)
	}

	req = newMultipartUploadRequest(t, http.MethodPost, "/api/libraries/simulation/upload", []testMultipartFile{
		{field: "files", filename: "voice.json", body: string(profileData)},
	})
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate upload status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}

	manifest, err := server.store.CreateProject("Load Simulation Library")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/library/load", bytes.NewBufferString(`{"name":"voice"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("load status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.importCalls != 1 {
		t.Fatalf("import calls = %d, want 1", fake.importCalls)
	}
	wantImportPath := filepath.Join(manifest.RootDir, "profiles", "imported", "voice.json")
	if fake.importPath != wantImportPath {
		t.Fatalf("import path = %q, want %q", fake.importPath, wantImportPath)
	}

	realManifest, err := server.store.CreateProject("Restore Simulation Library")
	if err != nil {
		t.Fatalf("CreateProject real: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+realManifest.ID+"/simulate/library/load", bytes.NewBufferString(`{"name":"voice"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("real load status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(realManifest.OutputDir, "meta", "simulation_profile.json")); err != nil {
		t.Fatalf("simulation profile was not imported into project output: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+realManifest.ID+"/snapshot", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var snapshot projectSnapshotResponse
	if err := json.NewDecoder(rec.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if snapshot.Simulation.ImportStatus != "done" {
		t.Fatalf("simulation import status = %q, want done", snapshot.Simulation.ImportStatus)
	}
	if snapshot.Simulation.ImportedFile == nil || snapshot.Simulation.ImportedFile.Name != "voice.json" {
		t.Fatalf("simulation imported file = %+v, want voice.json", snapshot.Simulation.ImportedFile)
	}
	if !strings.Contains(snapshot.Simulation.Message, "voice") {
		t.Fatalf("simulation message = %q, want profile name", snapshot.Simulation.Message)
	}
}

func TestProjectSimulationImportAlsoAddsLibraryEntry(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()
	manifest, err := server.store.CreateProject("Import Simulation Library")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	installFakeSession(t, server, manifest)

	profile := testWebSimulationProfile("imported.txt", "def456")
	profileData, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/import", []testMultipartFile{
		{field: "profile", filename: "auto.json", body: string(profileData)},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		LibrarySaved bool `json:"library_saved"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if !response.LibrarySaved {
		t.Fatalf("library_saved = false, want true")
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, simulationLibraryDirName, "auto.json")); err != nil {
		t.Fatalf("imported profile was not copied to library: %v", err)
	}
}

func TestNovelLibrarySaveLoadRewritesManifestAndSkipsAnalyze(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	sourceProject, err := server.store.CreateProject("Prepared Novel Source")
	if err != nil {
		t.Fatalf("CreateProject source: %v", err)
	}
	sourcePath := writePreparedAdaptationFixture(t, sourceProject, "source.txt")
	installFakeSession(t, server, sourceProject)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+sourceProject.ID+"/adapt/library/save", bytes.NewBufferString(`{"name":"Fixture Novel","source_file":"source.txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save status = %d body=%s", rec.Code, rec.Body.String())
	}
	requireLibraryEvent(t, server.sessions.Project(sourceProject.ID), "novel_save", "Fixture Novel")

	entryRoot := filepath.Join(runtimeRoot, novelLibraryDirName, "Fixture Novel")
	if _, err := os.Stat(filepath.Join(entryRoot, "source", novelLibrarySourceName)); err != nil {
		t.Fatalf("library source copy missing: %v", err)
	}
	libraryManifest := readAdaptationManifestForTest(t, filepath.Join(entryRoot, "meta", "adaptation", "source_manifest.json"))
	wantLibrarySource := filepath.Join(entryRoot, "source", novelLibrarySourceName)
	if filepath.Clean(libraryManifest.SourcePath) != filepath.Clean(wantLibrarySource) {
		t.Fatalf("library source_path = %q, want %q", libraryManifest.SourcePath, wantLibrarySource)
	}
	if filepath.Clean(libraryManifest.SourcePath) == filepath.Clean(sourcePath) {
		t.Fatalf("library source_path still points at original project source: %q", sourcePath)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+sourceProject.ID+"/adapt/library/save", bytes.NewBufferString(`{"name":"Fixture Novel","source_file":"source.txt"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate save status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}

	targetProject, err := server.store.CreateProject("Loaded Novel Target")
	if err != nil {
		t.Fatalf("CreateProject target: %v", err)
	}
	fake := installFakeSession(t, server, targetProject)
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+targetProject.ID+"/adapt/library/load", bytes.NewBufferString(`{"name":"Fixture Novel"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("load status = %d body=%s", rec.Code, rec.Body.String())
	}
	requireLibraryEvent(t, server.sessions.Project(targetProject.ID), "novel_load", "Fixture Novel")
	var loadResponse struct {
		Analyzed   bool            `json:"analyzed"`
		SourceFile apiUploadedFile `json:"source_file"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&loadResponse); err != nil {
		t.Fatalf("decode load response: %v", err)
	}
	if !loadResponse.Analyzed {
		t.Fatalf("loaded novel should be marked analyzed")
	}
	if loadResponse.SourceFile.RelativePath != "Fixture Novel.txt" {
		t.Fatalf("source relative path = %q, want Fixture Novel.txt", loadResponse.SourceFile.RelativePath)
	}
	if fake.adaptAnalyzeCalls != 0 {
		t.Fatalf("library load should not call analyze, calls=%d", fake.adaptAnalyzeCalls)
	}

	projectSourcePath := filepath.Join(targetProject.RootDir, "uploads", "adaptation", "Fixture Novel.txt")
	projectManifest := readAdaptationManifestForTest(t, filepath.Join(targetProject.OutputDir, "meta", "adaptation", "source_manifest.json"))
	if filepath.Clean(projectManifest.SourcePath) != filepath.Clean(projectSourcePath) {
		t.Fatalf("project source_path = %q, want %q", projectManifest.SourcePath, projectSourcePath)
	}
	if _, _, err := adaptengine.ValidatePreparedSource(store.NewStore(targetProject.OutputDir), projectSourcePath); err != nil {
		t.Fatalf("loaded prepared source does not validate: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+targetProject.ID+"/adapt/start", bytes.NewBufferString(`{"source_file":"Fixture Novel.txt","mode":"chapter","brief":"adapt this source"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptAnalyzeCalls != 0 {
		t.Fatalf("start after library load should not call analyze, calls=%d", fake.adaptAnalyzeCalls)
	}
	if fake.adaptStartCalls != 1 {
		t.Fatalf("adapt start calls = %d, want 1", fake.adaptStartCalls)
	}
}

func requireLibraryEvent(t *testing.T, session *ProjectSession, kind, name string) WebEvent {
	t.Helper()
	if session == nil {
		t.Fatalf("project session is nil")
	}
	for _, ev := range session.HistoryAfter(0) {
		if ev.Type != webEventTypeHostEvent || ev.Event == nil {
			continue
		}
		if ev.Event.Category == "LIBRARY" && ev.Event.Kind == kind && strings.Contains(ev.Event.Summary, name) {
			return ev
		}
	}
	t.Fatalf("library event kind=%q name=%q not found: %+v", kind, name, session.HistoryAfter(0))
	return WebEvent{}
}

func writePreparedAdaptationFixture(t *testing.T, manifest ProjectManifest, sourceName string) string {
	t.Helper()
	sourceText := "第一章 开端\nalpha source chapter one\n\n第二章 转折\nbeta source chapter two\n"
	sourcePath := filepath.Join(manifest.RootDir, "uploads", "adaptation", sourceName)
	if err := os.WriteFile(sourcePath, []byte(sourceText), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	st := store.NewStore(manifest.OutputDir)
	sourceOne, err := st.Adaptation.SaveSourceChapter(1, "开端", "alpha source chapter one")
	if err != nil {
		t.Fatalf("SaveSourceChapter 1: %v", err)
	}
	sourceTwo, err := st.Adaptation.SaveSourceChapter(2, "转折", "beta source chapter two")
	if err != nil {
		t.Fatalf("SaveSourceChapter 2: %v", err)
	}
	sourceManifest := domain.AdaptationSourceManifest{
		SourcePath:   sourcePath,
		ChapterCount: 2,
		Chapters:     []domain.AdaptationSource{sourceOne, sourceTwo},
	}
	if err := st.Adaptation.SaveSourceManifest(sourceManifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	reports := []domain.AdaptationSourceReport{
		{Chapter: 1, Title: "开端", SourceSHA256: sourceOne.SHA256, Summary: "source chapter one summary", KeyEvents: []string{"event one"}},
		{Chapter: 2, Title: "转折", SourceSHA256: sourceTwo.SHA256, Summary: "source chapter two summary", KeyEvents: []string{"event two"}},
	}
	for _, report := range reports {
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatalf("SaveSourceReport %d: %v", report.Chapter, err)
		}
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{
		Premise: "source premise",
		Characters: []domain.Character{{
			Name:        "Ari",
			Role:        "lead",
			Description: "lead character",
			Arc:         "changes under pressure",
			Traits:      []string{"decisive"},
		}},
		WorldRules: []domain.WorldRule{{Category: "world", Rule: "rule", Boundary: "boundary"}},
		Volumes: []domain.VolumeOutline{{
			Index: 1,
			Title: "Volume One",
			Theme: "pressure",
			Arcs: []domain.ArcOutline{{
				Index: 1,
				Title: "Arc One",
				Goal:  "survive",
				Chapters: []domain.OutlineEntry{{
					Chapter:   1,
					Title:     "开端",
					CoreEvent: "event one",
					Hook:      "hook one",
					Scenes:    []string{"scene one"},
				}},
			}},
		}},
	}); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	batch := domain.AdaptationCoCreateDossierBatch{
		Index:           1,
		SourceFrom:      1,
		SourceTo:        2,
		SourceSignature: store.AdaptationDossierBatchSpecs(sourceManifest, adaptengine.CoCreateDossierBatchSize, adaptengine.CoCreateDossierBatchRuneLimit)[0].SourceSignature,
		PromptVersion:   adaptengine.CoCreateDossierPromptVersion,
		PlotPhase:       "fixture source plot",
	}
	if err := st.Adaptation.SaveCoCreateDossierBatch(batch); err != nil {
		t.Fatalf("SaveCoCreateDossierBatch: %v", err)
	}
	if err := st.Adaptation.SaveCoCreateDossier(domain.AdaptationCoCreateDossier{
		Version:            1,
		PromptVersion:      adaptengine.CoCreateDossierPromptVersion,
		SourcePath:         sourcePath,
		SourceChapterCount: sourceManifest.ChapterCount,
		SourceSignature:    store.AdaptationSourceSignature(sourceManifest),
		BatchSize:          adaptengine.CoCreateDossierBatchSize,
		BatchRuneLimit:     adaptengine.CoCreateDossierBatchRuneLimit,
		Batches:            []domain.AdaptationCoCreateDossierBatch{batch},
	}); err != nil {
		t.Fatalf("SaveCoCreateDossier: %v", err)
	}
	return sourcePath
}

func readAdaptationManifestForTest(t *testing.T, path string) domain.AdaptationSourceManifest {
	t.Helper()
	var manifest domain.AdaptationSourceManifest
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read adaptation manifest %s: %v", path, err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode adaptation manifest %s: %v", path, err)
	}
	return manifest
}
