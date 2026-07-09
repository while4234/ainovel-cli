package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestStylesEndpointReturnsMarkdownHeadingLabels(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/styles", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("styles status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body apiStylesResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode styles: %v", err)
	}
	labels := make(map[string]string, len(body.Styles))
	for _, item := range body.Styles {
		labels[item.ID] = item.Label
	}
	if labels["fantasy"] != "奇幻冒险风格" || labels["default"] != "通用写作风格" {
		t.Fatalf("style labels = %+v", labels)
	}
}

func TestCreateProjectWithStyleWritesProjectOverlay(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Styled Novel","style":"fantasy"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	overlay := readProjectOverlay(t, manifest)
	if overlay.Style != "fantasy" {
		t.Fatalf("project style = %q, want fantasy", overlay.Style)
	}
}

func TestCreateProjectRejectsUnknownStyle(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Bad Style","style":"missing-style"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create unknown style status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown style") {
		t.Fatalf("unknown style response should explain validation: %s", rec.Body.String())
	}
}

func TestOpenProjectHostUsesProjectStyleOverride(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProjectWithStyle("Project Style", "fantasy")
	if err != nil {
		t.Fatalf("CreateProjectWithStyle: %v", err)
	}
	base := testWebConfig(t)
	base.Style = "default"

	h, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	defer h.Close()
	if snap := h.Snapshot(); snap.Style != "fantasy" {
		t.Fatalf("snapshot style = %q, want fantasy", snap.Style)
	}
}

func TestProjectStyleCanChangeBeforeWritingStarts(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProjectWithStyle("Fresh Style", "default")
	if err != nil {
		t.Fatalf("CreateProjectWithStyle: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/style", bytes.NewBufferString(`{"style":"romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("style switch status = %d body=%s", rec.Code, rec.Body.String())
	}
	overlay := readProjectOverlay(t, manifest)
	if overlay.Style != "romance" {
		t.Fatalf("project style = %q, want romance", overlay.Style)
	}
}

func TestProjectStyleCanChangeAfterProposalBeforeWritingStarts(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProjectWithStyle("Proposal Style", "default")
	if err != nil {
		t.Fatalf("CreateProjectWithStyle: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{
		NovelName:      "半熟恋人",
		Phase:          "ready",
		TotalChapters:  36,
		CompletedCount: 0,
		TotalWordCount: 0,
		RuntimeState:   "idle",
	}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/style", bytes.NewBufferString(`{"style":"romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("proposal style switch status = %d body=%s", rec.Code, rec.Body.String())
	}
	overlay := readProjectOverlay(t, manifest)
	if overlay.Style != "romance" {
		t.Fatalf("project style = %q, want romance", overlay.Style)
	}
}

func TestProjectStyleChangeRejectsStartedProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProjectWithStyle("Locked Style", "default")
	if err != nil {
		t.Fatalf("CreateProjectWithStyle: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{NovelName: "已开书", TotalChapters: 12, CompletedCount: 1, TotalWordCount: 3200}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/style", bytes.NewBufferString(`{"style":"suspense"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("locked style status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	overlay := readProjectOverlay(t, manifest)
	if overlay.Style != "default" {
		t.Fatalf("locked project style changed to %q", overlay.Style)
	}
}
