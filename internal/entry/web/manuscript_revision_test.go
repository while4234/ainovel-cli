package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestLegacyChapterReviseCreatesSafePreviewWithoutResume(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
	defer server.Close()
	manifest, err := server.store.CreateProject("safe manuscript revision")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	chapterID := "ch_abcdef0123456789abcdef0123456789"
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "第一章", CoreEvent: "选择", Hook: "代价"}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Progress.Init("书", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "正式正文"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.manuscriptService = host.NewManuscriptRevisionServiceWithRuntime(st, noChangeManuscriptWriter{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/chapters/revise", bytes.NewBufferString(`{"chapter":1,"instruction":"打磨节奏","mode":"polish"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("legacy revise status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		AwaitingConfirmation bool `json:"awaiting_confirmation"`
		Running              bool `json:"running"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !response.AwaitingConfirmation || response.Running {
		t.Fatalf("unsafe legacy response: %+v", response)
	}
	if prose, _ := st.Drafts.LoadChapterText(1); prose != "正式正文" {
		t.Fatalf("legacy preview changed current prose: %q", prose)
	}
	if active, err := st.ManuscriptRevisions.Active(); err != nil || active == nil || active.Baseline.ChapterID != chapterID {
		t.Fatalf("active preview = %+v err=%v", active, err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/manuscript/chapters/"+chapterID, nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"content":"正式正文"`)) {
		t.Fatalf("stable chapter read status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type noChangeManuscriptWriter struct{}

func (noChangeManuscriptWriter) PlanManuscriptRevision(context.Context, domain.ManuscriptBaseline, string, domain.ManuscriptInstructionKind) (host.ManuscriptPlan, error) {
	return host.ManuscriptPlan{}, nil
}
func (noChangeManuscriptWriter) GenerateManuscriptSegment(context.Context, domain.ManuscriptRevisionRuntime, domain.ManuscriptReworkItem, host.ManuscriptGenerationContext, int, int, string) (host.ManuscriptGeneratedSegment, error) {
	return host.ManuscriptGeneratedSegment{}, nil
}
