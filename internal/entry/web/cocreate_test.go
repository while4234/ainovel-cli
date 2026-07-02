package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestProjectCoCreateSuggestionsAndCommitUseDraftPrompt(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("可以开始。", "## 主题\n- 月城追凶", true, "增加双主角", "改成赛博城市")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"写一个月城悬疑"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode begin response: %v", err)
	}
	if !response.CoCreate.Ready || response.CoCreate.DraftPrompt != "## 主题\n- 月城追凶" {
		t.Fatalf("co-create state = %+v", response.CoCreate)
	}
	if len(response.CoCreate.Suggestions) != 2 || response.CoCreate.Suggestions[0] != "增加双主角" {
		t.Fatalf("suggestions = %v", response.CoCreate.Suggestions)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.prepareRulesCalls != 1 || fake.preparedRulesPrompt != "## 主题\n- 月城追凶" {
		t.Fatalf("PrepareUserRules calls=%d prompt=%q", fake.prepareRulesCalls, fake.preparedRulesPrompt)
	}
	if fake.startPreparedCalls != 1 {
		t.Fatalf("StartPrepared calls = %d, want 1", fake.startPreparedCalls)
	}
	if !strings.Contains(fake.startPreparedPrompt, "[创作要求]\n## 主题\n- 月城追凶") {
		t.Fatalf("StartPrepared prompt should wrap draft prompt, got %q", fake.startPreparedPrompt)
	}
}

func TestProjectStageCoCreatePausesAndResumesWithDraft(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Stage CoCreate")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.stageCoCreateReply = webCoCreateReply("后续方向已清楚。", "## 后续走向\n- 回收旧伏笔", true)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"stage"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stage begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.pauseCoCreateCalls != 1 {
		t.Fatalf("PauseForCoCreate calls = %d, want 1", fake.pauseCoCreateCalls)
	}
	if fake.stageCoCreateCalls != 1 {
		t.Fatalf("StageCoCreateStream calls = %d, want 1", fake.stageCoCreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stage commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.resumeCoCreateCalls != 1 || fake.resumeCoCreateDraft != "## 后续走向\n- 回收旧伏笔" {
		t.Fatalf("ResumeFromCoCreate calls=%d draft=%q", fake.resumeCoCreateCalls, fake.resumeCoCreateDraft)
	}
}

func TestProjectStageCoCreateFailureKeepsStateCancelable(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Stage CoCreate Failure")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.stageCoCreateErr = errors.New("stream failed")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"stage"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("stage begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	var failure struct {
		Error    string           `json:"error"`
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&failure); err != nil {
		t.Fatalf("decode failure response: %v", err)
	}
	if !strings.Contains(failure.Error, "stream failed") {
		t.Fatalf("error = %q, want stream failed", failure.Error)
	}
	if !failure.CoCreate.Active || failure.CoCreate.Kind != webCoCreateKindStage {
		t.Fatalf("failure co-create state = %+v, want active stage", failure.CoCreate)
	}
	if fake.pauseCoCreateCalls != 1 {
		t.Fatalf("PauseForCoCreate calls = %d, want 1", fake.pauseCoCreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/cancel", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.cancelCoCreateCalls != 1 {
		t.Fatalf("CancelCoCreate calls = %d, want 1", fake.cancelCoCreateCalls)
	}
}

func TestProjectAdaptCoCreateLocksSelectedModeOnCommit(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(t.TempDir(), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	fake.adaptCoCreateReply = webCoCreateReply(
		"目标明确，可以开始。",
		"## 改编模式\n\ngranularity=chapter\nrewrite_policy=preserve_details\n\n## 用户目标\n- 加强女主线",
		true,
	)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"arc"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 1 {
		t.Fatalf("AdaptCoCreateStream calls = %d, want 1", fake.adaptCoCreateCalls)
	}
	if len(fake.lastCoCreateHistory) == 0 || !strings.Contains(fake.lastCoCreateHistory[0].Content, "granularity=arc") {
		t.Fatalf("adapt opener did not lock arc mode: %+v", fake.lastCoCreateHistory)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("adapt commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.prepareExternalRulesCalls != 1 {
		t.Fatalf("PrepareExternalSourceUserRules calls = %d, want 1", fake.prepareExternalRulesCalls)
	}
	if fake.adaptStartCalls != 1 {
		t.Fatalf("StartAdaptationPrepared calls = %d, want 1", fake.adaptStartCalls)
	}
	if fake.adaptOptions.Granularity != domain.AdaptationGranularityArc {
		t.Fatalf("adapt granularity = %q, want arc", fake.adaptOptions.Granularity)
	}
	if fake.adaptOptions.RewritePolicy != domain.AdaptationRewriteFullRewrite {
		t.Fatalf("adapt rewrite policy = %q, want full_rewrite", fake.adaptOptions.RewritePolicy)
	}
	wantSourcePath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source.txt")
	if fake.adaptOptions.SourcePath != wantSourcePath {
		t.Fatalf("adapt source path = %q, want %q", fake.adaptOptions.SourcePath, wantSourcePath)
	}
}

func webCoCreateReply(message, draft string, ready bool, suggestions ...string) host.CoCreateReply {
	return host.CoCreateReply{
		Message:     message,
		Prompt:      draft,
		Ready:       ready,
		Suggestions: suggestions,
		Raw:         "<reply>" + message + "</reply><draft>" + draft + "</draft><ready>true</ready><suggestions></suggestions>",
	}
}
