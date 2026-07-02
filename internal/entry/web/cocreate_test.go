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
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
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

func TestProjectCoCreatePersistsTargetTotalWords(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Budget")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("可以开始。", "## 主题\n- 5000 字短篇", true)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"写一部短篇","target_total_words":5000}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.setWordBudgetCalls != 1 || fake.wordBudget == nil || fake.wordBudget.TargetTotalWords != 5000 {
		t.Fatalf("SetWordBudget calls=%d budget=%+v", fake.setWordBudgetCalls, fake.wordBudget)
	}
	if !strings.Contains(fake.startPreparedPrompt, "target_total_words=5000") {
		t.Fatalf("start prompt missing word budget contract: %q", fake.startPreparedPrompt)
	}
}

func TestProjectCoCreateWebCopyUsesStartButtonInsteadOfCtrlS(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Copy")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateProgress = []coCreateProgressStep{{kind: "reply", text: "可以按 Ctrl+S 开始创作。"}}
	fake.cocreateReply = webCoCreateReply("可以按 Ctrl+S 开始创作。", "## 主题\n- 月城追凶", true)

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
	got := response.CoCreate.StreamReply + "\n"
	for _, message := range response.CoCreate.Messages {
		got += message.Content + "\n"
	}
	if strings.Contains(got, "Ctrl+S") {
		t.Fatalf("web co-create copy should not mention Ctrl+S: %q", got)
	}
	if !strings.Contains(got, "点击「启动」") {
		t.Fatalf("web co-create copy should mention start button: %q", got)
	}
}

func TestProjectCoCreateSuggestionSendAndReviseTruncatesHistory(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Revise")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("继续确认。", "## 方向\n- 初始", false, "加强女主线")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"写月城悬疑"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"加强女主线","source":"suggestion"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("send status = %d body=%s", rec.Code, rec.Body.String())
	}
	var sent struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&sent); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	var suggestionMessage webCoCreateMessage
	for _, message := range sent.CoCreate.Messages {
		if message.Role == "user" && message.Content == "加强女主线" {
			suggestionMessage = message
			break
		}
	}
	if suggestionMessage.ID == "" || !suggestionMessage.Editable || suggestionMessage.Source != "suggestion" {
		t.Fatalf("suggestion message metadata = %+v", suggestionMessage)
	}
	if fake.cocreateCalls != 2 {
		t.Fatalf("co-create calls after send = %d, want 2", fake.cocreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/revise", bytes.NewBufferString(`{"message_id":`+strconvQuote(suggestionMessage.ID)+`,"text":"加强女主线，但保留慢热节奏"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revise status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.cocreateCalls != 3 {
		t.Fatalf("co-create calls after revise = %d, want 3", fake.cocreateCalls)
	}
	if len(fake.lastCoCreateHistory) != 3 {
		t.Fatalf("history after revise = %+v, want 3 entries", fake.lastCoCreateHistory)
	}
	if got := fake.lastCoCreateHistory[2].Content; got != "加强女主线，但保留慢热节奏" {
		t.Fatalf("revised history content = %q", got)
	}
	var revised struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&revised); err != nil {
		t.Fatalf("decode revise response: %v", err)
	}
	for _, message := range revised.CoCreate.Messages {
		if message.Content == "加强女主线" {
			t.Fatalf("old suggestion survived after revise: %+v", revised.CoCreate.Messages)
		}
	}
}

func TestProjectCoCreateRejectsInvalidSourceAndNonEditableRevise(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Rejects")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("先确认。", "## 方向", false)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"写月城悬疑"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	var begun struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&begun); err != nil {
		t.Fatalf("decode begin response: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"随便补充","source":"bad"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid source status = %d body=%s", rec.Code, rec.Body.String())
	}

	assistantID := ""
	for _, message := range begun.CoCreate.Messages {
		if message.Role == "assistant" {
			assistantID = message.ID
			break
		}
	}
	if assistantID == "" {
		t.Fatalf("assistant message id not found: %+v", begun.CoCreate.Messages)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/revise", bytes.NewBufferString(`{"message_id":`+strconvQuote(assistantID)+`,"text":"改掉 AI 消息"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-editable revise status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProjectStageCoCreatePausesAndResumesWithDraft(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
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
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
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
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
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

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"arc","initial":"Keep the ending, but make the heroine survive."}`))
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
	if len(fake.lastCoCreateHistory) < 2 || fake.lastCoCreateHistory[1].Content != "Keep the ending, but make the heroine survive." {
		t.Fatalf("adapt initial brief was not sent to model history: %+v", fake.lastCoCreateHistory)
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
	if fake.adaptStartCalls != 0 {
		t.Fatalf("StartAdaptationPrepared calls = %d, want 0 before confirm", fake.adaptStartCalls)
	}
	if fake.adaptProposalCalls != 1 {
		t.Fatalf("BuildAdaptationProposal calls = %d, want 1", fake.adaptProposalCalls)
	}
	if fake.adaptProposalOptions.Granularity != domain.AdaptationGranularityArc {
		t.Fatalf("adapt granularity = %q, want arc", fake.adaptProposalOptions.Granularity)
	}
	if fake.adaptProposalOptions.RewritePolicy != domain.AdaptationRewriteFullRewrite {
		t.Fatalf("adapt rewrite policy = %q, want full_rewrite", fake.adaptProposalOptions.RewritePolicy)
	}
	wantSourcePath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source.txt")
	if fake.adaptProposalOptions.SourcePath != wantSourcePath {
		t.Fatalf("adapt source path = %q, want %q", fake.adaptProposalOptions.SourcePath, wantSourcePath)
	}
	var response struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode commit response: %v", err)
	}
	if response.CoCreate.Proposal == nil || response.CoCreate.Proposal.Status != domain.AdaptationPlanStatusProposal {
		t.Fatalf("adapt commit should return proposal, got %+v", response.CoCreate.Proposal)
	}
}

func TestProjectAdaptCoCreateRejectsTargetTotalWords(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt Budget Reject")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"arc","target_total_words":5000}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("adapt begin status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 0 {
		t.Fatalf("adapt co-create should not start, calls=%d", fake.adaptCoCreateCalls)
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
