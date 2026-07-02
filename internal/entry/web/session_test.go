package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/grokauth"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
)

func TestSessionManagerReusesActiveProjectHostConcurrently(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProject("Concurrent Session")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	manager := NewSessionManager(testWebConfig(t), assets.Load("default"), store)
	defer manager.CloseAll()

	const workers = 12
	sessions := make(chan *ProjectSession, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, _, err := manager.Open(manifest.ID)
			if err != nil {
				t.Errorf("Open: %v", err)
				return
			}
			sessions <- session
		}()
	}
	wg.Wait()
	close(sessions)

	var first *ProjectSession
	for session := range sessions {
		if first == nil {
			first = session
			continue
		}
		if session != first {
			t.Fatalf("expected one active session, got %p and %p", first, session)
		}
	}
	active := manager.ActiveProjectIDs()
	if len(active) != 1 || active[0] != manifest.ID {
		t.Fatalf("active projects = %v, want [%s]", active, manifest.ID)
	}
}

func TestProjectSessionRejectsConcurrentResumeContinue(t *testing.T) {
	fake := newFakeProjectHost()
	fake.resumeStarted = make(chan struct{})
	fake.releaseResume = make(chan struct{})

	session, err := NewProjectSession(ProjectManifest{ID: "project-1"}, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	resumeErr := make(chan error, 1)
	go func() {
		_, err := session.Resume()
		resumeErr <- err
	}()

	select {
	case <-fake.resumeStarted:
	case <-time.After(time.Second):
		t.Fatal("resume did not enter host call")
	}

	if err := session.Continue("keep going"); !errors.Is(err, ErrSessionActionInProgress) {
		t.Fatalf("concurrent continue error = %v, want %v", err, ErrSessionActionInProgress)
	}
	close(fake.releaseResume)

	select {
	case err := <-resumeErr:
		if err != nil {
			t.Fatalf("resume returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("resume did not complete")
	}
	if fake.continueCalls != 0 {
		t.Fatalf("concurrent continue reached host %d time(s)", fake.continueCalls)
	}
	if fake.resumeCalls != 1 {
		t.Fatalf("resume host calls = %d, want 1", fake.resumeCalls)
	}
}

func TestProjectSessionAllowsModelSwitchDuringAction(t *testing.T) {
	fake := newFakeProjectHost()
	fake.resumeStarted = make(chan struct{})
	fake.releaseResume = make(chan struct{})

	session, err := NewProjectSession(ProjectManifest{ID: "project-1"}, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	resumeErr := make(chan error, 1)
	go func() {
		_, err := session.Resume()
		resumeErr <- err
	}()

	select {
	case <-fake.resumeStarted:
	case <-time.After(time.Second):
		t.Fatal("resume did not enter host call")
	}

	if _, err := session.SwitchModel("writer", "proxy-openai", "deepseek-chat"); err != nil {
		t.Fatalf("model switch during action returned error: %v", err)
	}
	if fake.switchCalls != 1 || fake.switchRole != "writer" || fake.switchProvider != "proxy-openai" || fake.switchModel != "deepseek-chat" {
		t.Fatalf("switch args calls=%d role=%q provider=%q model=%q", fake.switchCalls, fake.switchRole, fake.switchProvider, fake.switchModel)
	}
	close(fake.releaseResume)

	select {
	case err := <-resumeErr:
		if err != nil {
			t.Fatalf("resume returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("resume did not complete")
	}
}

func TestProjectSessionPauseCancelsAdaptationAnalysis(t *testing.T) {
	fake := newFakeProjectHost()
	fake.adaptAnalyzeStarted = make(chan struct{})
	fake.blockAdaptAnalyze = true

	session, err := NewProjectSession(ProjectManifest{ID: "project-1"}, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	analysisErr := make(chan error, 1)
	go func() {
		_, err := session.PrepareAdaptationSource(context.Background(), "source.txt")
		analysisErr <- err
	}()

	select {
	case <-fake.adaptAnalyzeStarted:
	case <-time.After(time.Second):
		t.Fatal("adaptation analysis did not start")
	}

	if !session.Pause() {
		t.Fatal("Pause should report a canceled adaptation action")
	}

	select {
	case err := <-analysisErr:
		var paused adaptationPausedError
		if !errors.As(err, &paused) {
			t.Fatalf("analysis error = %v, want adaptationPausedError", err)
		}
	case <-time.After(time.Second):
		t.Fatal("adaptation analysis did not stop after pause")
	}
}

func TestProjectSessionUpsertsHostEventsByID(t *testing.T) {
	session := newTestSessionWithoutHost("project-1")
	start := time.Now().UTC()
	finish := start.Add(2 * time.Second)

	first := session.appendHostEvent(host.Event{
		ID:       "tool-1",
		Time:     start,
		Category: "TOOL",
		Agent:    "writer",
		Summary:  "draft_chapter",
		Level:    "info",
	})
	second := session.appendHostEvent(host.Event{
		ID:         "tool-1",
		Time:       start,
		FinishedAt: finish,
		Category:   "TOOL",
		Agent:      "writer",
		Summary:    "draft_chapter done",
		Level:      "success",
	})

	if second.Seq <= first.Seq {
		t.Fatalf("updated event should receive a newer seq: first=%d second=%d", first.Seq, second.Seq)
	}
	history := session.HistoryAfter(0)
	if len(history) != 1 {
		t.Fatalf("history length = %d, want one upserted row: %+v", len(history), history)
	}
	if history[0].HostEventID != "tool-1" || history[0].Event.Running {
		t.Fatalf("event was not updated in place: %+v", history[0])
	}
	if history[0].Event.Summary != "draft_chapter done" {
		t.Fatalf("summary = %q, want final summary", history[0].Event.Summary)
	}
}

func TestProjectSessionServeEventsHonorsAfter(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProject("SSE After")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	manager := NewSessionManager(testWebConfig(t), assets.Load("default"), store)
	defer manager.CloseAll()
	session, _, err := manager.Open(manifest.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	old := session.appendStreamDelta("old delta")
	session.appendStreamDelta("new delta")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/events?after=0", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	if err := session.ServeEvents(req.Context(), rec, old.Seq); err != nil {
		t.Fatalf("ServeEvents: %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "old delta") {
		t.Fatalf("SSE replay included event at after seq %d: %s", old.Seq, body)
	}
	if !strings.Contains(body, "new delta") {
		t.Fatalf("SSE replay did not include newer event: %s", body)
	}
	if !strings.Contains(body, "event: snapshot") {
		t.Fatalf("SSE replay should include current snapshot: %s", body)
	}
}

func TestProjectSessionPublishesCoCreateProgressWithoutWritingStream(t *testing.T) {
	fake := newFakeProjectHost()
	fake.cocreateProgress = []coCreateProgressStep{
		{kind: host.CoCreateProgressThinking, text: "checking premise"},
		{kind: host.CoCreateProgressReply, text: "先确认主角目标"},
	}
	fake.cocreateReply = host.CoCreateReply{
		Message: "先确认主角目标",
		Prompt:  "## 方向\n- 主角寻找失踪同伴",
		Ready:   false,
		Raw:     "<reply>先确认主角目标</reply><draft>## 方向\n- 主角寻找失踪同伴</draft><ready>false</ready><suggestions></suggestions>",
	}
	session, err := NewProjectSession(ProjectManifest{ID: "project-1"}, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	state, err := session.BeginCoCreate(context.Background(), webCoCreateBeginRequest{
		Kind:    webCoCreateKindNormal,
		Initial: "写一个月城悬疑",
	})
	if err != nil {
		t.Fatalf("BeginCoCreate: %v", err)
	}
	if state.StreamReply != "" || len(state.Messages) != 2 {
		t.Fatalf("final co-create state should clear preview and keep one assistant message: %+v", state)
	}

	var sawThinking, sawReply, sawStreamDelta bool
	for _, ev := range session.HistoryAfter(0) {
		if ev.Type == webEventTypeStreamDelta || ev.Type == webEventTypeStreamClear {
			sawStreamDelta = true
		}
		if ev.Type != webEventTypeCoCreate || ev.CoCreate == nil {
			continue
		}
		if ev.CoCreate.StreamThinking == "checking premise" {
			sawThinking = true
		}
		if ev.CoCreate.StreamReply == "先确认主角目标" {
			sawReply = true
		}
	}
	if !sawThinking || !sawReply {
		t.Fatalf("co-create progress events missing thinking=%v reply=%v", sawThinking, sawReply)
	}
	if sawStreamDelta {
		t.Fatal("co-create progress polluted the main writing stream")
	}
}

func TestProjectSessionAppendRacesWithUnsubscribe(t *testing.T) {
	for iteration := range 100 {
		session := newTestSessionWithoutHost("project-1")
		unsubscribes := make([]func(), 0, 64)
		for range 64 {
			_, _, unsubscribe := session.Subscribe(0)
			unsubscribes = append(unsubscribes, unsubscribe)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for appendIndex := range 16 {
				session.appendStreamDelta(fmt.Sprintf("iteration %d append %d", iteration, appendIndex))
			}
		}()
		for _, unsubscribe := range unsubscribes {
			wg.Add(1)
			go func(unsubscribe func()) {
				defer wg.Done()
				<-start
				unsubscribe()
			}(unsubscribe)
		}

		close(start)
		wg.Wait()
	}
}

func TestProjectSessionAppendRacesWithClose(t *testing.T) {
	for iteration := range 100 {
		session := newTestSessionWithoutHost("project-1")
		for range 64 {
			session.Subscribe(0)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for appendIndex := range 16 {
				session.appendStreamDelta(fmt.Sprintf("iteration %d append %d", iteration, appendIndex))
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			session.Close()
		}()

		close(start)
		wg.Wait()
	}
}

func TestProjectSessionPumpExitsWhenHostClosesWithPendingDone(t *testing.T) {
	host := newFakeProjectHost()
	host.done = make(chan struct{}, 1)
	host.done <- struct{}{}

	session := newTestSessionWithoutHost("project-1")
	session.host = host

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		session.pump()
	}()

	host.Close()
	select {
	case <-pumpDone:
	case <-time.After(time.Second):
		t.Fatal("ProjectSession.pump did not exit after host channels closed")
	}
}

func TestProjectSteerAPIPropagatesHostErrors(t *testing.T) {
	cases := []struct {
		name    string
		running bool
		errText string
	}{
		{
			name:    "running inject failure",
			running: true,
			errText: "steer inject: coordinator rejected message",
		},
		{
			name:    "idle pending steer persistence failure",
			running: false,
			errText: "set pending steer: disk is read-only",
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

			fake := newFakeProjectHost()
			fake.snapshot = host.UISnapshot{IsRunning: c.running}
			fake.steerErr = errors.New(c.errText)
			session, err := NewProjectSession(manifest, fake)
			if err != nil {
				t.Fatalf("NewProjectSession: %v", err)
			}
			server.sessions.mu.Lock()
			server.sessions.sessions[manifest.ID] = session
			server.sessions.mu.Unlock()

			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/steer", bytes.NewBufferString(`{"text":"change course"}`))
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("steer status = %d body=%s, want 500", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.errText) {
				t.Fatalf("steer body %q does not contain %q", rec.Body.String(), c.errText)
			}
		})
	}
}

func TestProjectAPIErrorPaths(t *testing.T) {
	handler := NewHandler(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))

	req := httptest.NewRequest(http.MethodGet, "/api/projects/bad..id/snapshot", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalid id status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/project-1/events?after=abc", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid after status = %d body=%s", rec.Code, rec.Body.String())
	}

	projectID := createProjectViaAPI(t, handler, "Needs Text")
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/continue", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing continue text status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func createProjectViaAPI(t *testing.T, handler http.Handler, name string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":`+strconvQuote(name)+`}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body=%s", rec.Code, rec.Body.String())
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	return manifest.ID
}

func newTestSessionWithoutHost(projectID string) *ProjectSession {
	return &ProjectSession{
		manifest:    ProjectManifest{ID: projectID},
		hostEventAt: make(map[string]int),
		subscribers: make(map[chan WebEvent]struct{}),
	}
}

type fakeProjectHost struct {
	mu sync.Mutex

	snapshot host.UISnapshot

	resumeStarted              chan struct{}
	resumeStartedOnce          sync.Once
	releaseResume              chan struct{}
	resumeErr                  error
	continueErr                error
	steerErr                   error
	simulateErr                error
	importErr                  error
	importNovelErr             error
	adaptAnalyzeErr            error
	adaptProposalErr           error
	adaptConfirmErr            error
	adaptStartErr              error
	exportErr                  error
	cocreateErr                error
	stageCoCreateErr           error
	adaptCoCreateErr           error
	prepareUserRulesErr        error
	prepareExternalRulesErr    error
	setWordBudgetErr           error
	startPreparedErr           error
	resumeFromCoCreateErr      error
	requireAnalyzedAdaptSource bool
	blockAdaptAnalyze          bool

	resumeCalls                 int
	continueCalls               int
	steerCalls                  int
	simulateCalls               int
	importCalls                 int
	importNovelCalls            int
	adaptAnalyzeCalls           int
	adaptProposalCalls          int
	adaptConfirmCalls           int
	adaptStartCalls             int
	exportCalls                 int
	abortCalls                  int
	prepareRulesCalls           int
	prepareExternalRulesCalls   int
	setWordBudgetCalls          int
	startPreparedCalls          int
	cocreateCalls               int
	stageCoCreateCalls          int
	adaptCoCreateCalls          int
	pauseCoCreateCalls          int
	resumeCoCreateCalls         int
	cancelCoCreateCalls         int
	closeCalls                  int
	simulateDir                 string
	importPath                  string
	importNovelPath             string
	importNovelResumeFrom       int
	adaptAnalyzeStarted         chan struct{}
	adaptSourcePath             string
	adaptProposalOptions        adapt.ProposalOptions
	adaptProposal               *domain.AdaptationPlan
	adaptConfirmedPlan          *domain.AdaptationPlan
	adaptOptions                adapt.ProposalOptions
	exportOptions               exp.Options
	addProviderRole             string
	addProviderName             string
	addProviderConfig           bootstrap.ProviderConfig
	addProviderModel            string
	switchRole                  string
	switchProvider              string
	switchModel                 string
	grokStartAccountID          string
	grokStartAccountName        string
	grokCompleteCallback        string
	grokStatusAccountID         string
	preparedRulesPrompt         string
	preparedExternalRulesPrompt string
	wordBudget                  *domain.WordBudget
	startPreparedPrompt         string
	resumeCoCreateDraft         string
	lastCoCreateHistory         []host.CoCreateMessage
	cocreateReply               host.CoCreateReply
	stageCoCreateReply          host.CoCreateReply
	adaptCoCreateReply          host.CoCreateReply
	cocreateProgress            []coCreateProgressStep
	pauseCoCreateOK             bool
	abortOK                     bool
	exportResult                *exp.Result
	addProviderErr              error
	switchCalls                 int
	grokLoginStart              grokauth.LoginStart
	grokLoginPoll               grokauth.LoginPoll
	grokCompleteStatus          grokauth.AuthStatus
	grokStatus                  grokauth.AuthStatus

	events    chan host.Event
	stream    chan string
	done      chan struct{}
	closeOnce sync.Once
}

type coCreateProgressStep struct {
	kind string
	text string
}

func newFakeProjectHost() *fakeProjectHost {
	return &fakeProjectHost{
		events:          make(chan host.Event),
		stream:          make(chan string),
		done:            make(chan struct{}),
		pauseCoCreateOK: true,
		abortOK:         true,
	}
}

func (f *fakeProjectHost) Snapshot() host.UISnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func (f *fakeProjectHost) PrepareUserRules(prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepareRulesCalls++
	f.preparedRulesPrompt = prompt
	return f.prepareUserRulesErr
}

func (f *fakeProjectHost) PrepareExternalSourceUserRules(prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepareExternalRulesCalls++
	f.preparedExternalRulesPrompt = prompt
	return f.prepareExternalRulesErr
}

func (f *fakeProjectHost) SetWordBudget(budget *domain.WordBudget) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setWordBudgetCalls++
	if budget == nil {
		f.wordBudget = nil
	} else {
		copy := *budget
		f.wordBudget = &copy
	}
	return f.setWordBudgetErr
}

func (f *fakeProjectHost) StartPrepared(prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startPreparedCalls++
	f.startPreparedPrompt = prompt
	return f.startPreparedErr
}

func (f *fakeProjectHost) Abort() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.abortCalls++
	return f.abortOK
}

func (f *fakeProjectHost) Resume() (string, error) {
	f.mu.Lock()
	f.resumeCalls++
	started := f.resumeStarted
	f.mu.Unlock()

	if started != nil {
		f.resumeStartedOnce.Do(func() {
			close(started)
		})
	}
	if f.releaseResume != nil {
		<-f.releaseResume
	}
	if f.resumeErr != nil {
		return "", f.resumeErr
	}
	return "resume test label", nil
}

func (f *fakeProjectHost) Continue(string) error {
	f.mu.Lock()
	f.continueCalls++
	defer f.mu.Unlock()
	return f.continueErr
}

func (f *fakeProjectHost) Steer(string) error {
	f.mu.Lock()
	f.steerCalls++
	defer f.mu.Unlock()
	return f.steerErr
}

func (f *fakeProjectHost) CoCreateStream(_ context.Context, history []host.CoCreateMessage, onProgress func(kind, text string)) (host.CoCreateReply, error) {
	f.mu.Lock()
	f.cocreateCalls++
	f.lastCoCreateHistory = append([]host.CoCreateMessage(nil), history...)
	reply := f.cocreateReply
	err := f.cocreateErr
	progress := append([]coCreateProgressStep(nil), f.cocreateProgress...)
	f.mu.Unlock()
	emitCoCreateProgress(progress, onProgress)
	return reply, err
}

func (f *fakeProjectHost) StageCoCreateStream(_ context.Context, history []host.CoCreateMessage, onProgress func(kind, text string)) (host.CoCreateReply, error) {
	f.mu.Lock()
	f.stageCoCreateCalls++
	f.lastCoCreateHistory = append([]host.CoCreateMessage(nil), history...)
	reply := f.stageCoCreateReply
	err := f.stageCoCreateErr
	progress := append([]coCreateProgressStep(nil), f.cocreateProgress...)
	f.mu.Unlock()
	emitCoCreateProgress(progress, onProgress)
	return reply, err
}

func (f *fakeProjectHost) AdaptCoCreateStream(_ context.Context, history []host.CoCreateMessage, onProgress func(kind, text string)) (host.CoCreateReply, error) {
	f.mu.Lock()
	f.adaptCoCreateCalls++
	f.lastCoCreateHistory = append([]host.CoCreateMessage(nil), history...)
	reply := f.adaptCoCreateReply
	err := f.adaptCoCreateErr
	progress := append([]coCreateProgressStep(nil), f.cocreateProgress...)
	f.mu.Unlock()
	emitCoCreateProgress(progress, onProgress)
	return reply, err
}

func emitCoCreateProgress(progress []coCreateProgressStep, onProgress func(kind, text string)) {
	if onProgress == nil {
		return
	}
	for _, step := range progress {
		onProgress(step.kind, step.text)
	}
}

func (f *fakeProjectHost) PauseForCoCreate() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauseCoCreateCalls++
	return f.pauseCoCreateOK
}

func (f *fakeProjectHost) ResumeFromCoCreate(draft string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeCoCreateCalls++
	f.resumeCoCreateDraft = draft
	return f.resumeFromCoCreateErr
}

func (f *fakeProjectHost) CancelCoCreate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCoCreateCalls++
}

func (f *fakeProjectHost) ImportFrom(_ context.Context, opts imp.Options) (<-chan imp.Event, error) {
	f.mu.Lock()
	f.importNovelCalls++
	f.importNovelPath = opts.SourcePath
	f.importNovelResumeFrom = opts.ResumeFrom
	err := f.importNovelErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	events := make(chan imp.Event, 2)
	events <- imp.Event{Stage: imp.StageDone, Current: 1, Total: 1, Message: "novel imported"}
	close(events)
	return events, nil
}

func (f *fakeProjectHost) SimulateFromDir(_ context.Context, dir string) (<-chan sim.Event, error) {
	f.mu.Lock()
	f.simulateCalls++
	f.simulateDir = dir
	err := f.simulateErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	events := make(chan sim.Event, 2)
	events <- sim.Event{Stage: sim.StageDone, Message: "simulation complete"}
	close(events)
	return events, nil
}

func (f *fakeProjectHost) ImportSimulationProfile(_ context.Context, path string) (<-chan sim.Event, error) {
	f.mu.Lock()
	f.importCalls++
	f.importPath = path
	err := f.importErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	events := make(chan sim.Event, 2)
	events <- sim.Event{Stage: sim.StageDone, Message: "profile imported"}
	close(events)
	return events, nil
}

func (f *fakeProjectHost) PrepareAdaptationSource(_ context.Context, sourcePath string) (<-chan adapt.Event, error) {
	f.mu.Lock()
	f.adaptAnalyzeCalls++
	f.adaptSourcePath = sourcePath
	err := f.adaptAnalyzeErr
	started := f.adaptAnalyzeStarted
	block := f.blockAdaptAnalyze
	if started != nil {
		f.adaptAnalyzeStarted = nil
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if started != nil {
		close(started)
	}
	if block {
		return make(chan adapt.Event), nil
	}
	events := make(chan adapt.Event, 2)
	events <- adapt.Event{Stage: adapt.StageDone, Message: "adaptation source analyzed"}
	close(events)
	return events, nil
}

func (f *fakeProjectHost) StartAdaptationPreparedWithOptions(options adapt.ProposalOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adaptStartCalls++
	f.adaptOptions = options
	if f.requireAnalyzedAdaptSource && options.SourcePath != f.adaptSourcePath {
		return fmt.Errorf("adaptation source %q has not completed analysis", options.SourcePath)
	}
	return f.adaptStartErr
}

func (f *fakeProjectHost) BuildAdaptationProposal(options adapt.ProposalOptions) (*domain.AdaptationPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adaptProposalCalls++
	f.adaptProposalOptions = options
	if f.requireAnalyzedAdaptSource && options.SourcePath != f.adaptSourcePath {
		return nil, fmt.Errorf("adaptation source %q has not completed analysis", options.SourcePath)
	}
	if f.adaptProposalErr != nil {
		return nil, f.adaptProposalErr
	}
	if f.adaptProposal != nil {
		copy := *f.adaptProposal
		return &copy, nil
	}
	return &domain.AdaptationPlan{
		Granularity:   options.Granularity,
		Status:        domain.AdaptationPlanStatusProposal,
		RewritePolicy: options.RewritePolicy,
		Brief:         options.Brief,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "Target One",
			SourceChapters: []int{1},
			OutlineEntry: domain.OutlineEntry{
				Chapter:   1,
				Title:     "Target One",
				CoreEvent: "target event",
			},
		}},
	}, nil
}

func (f *fakeProjectHost) ConfirmAdaptationProposal() (*domain.AdaptationPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adaptConfirmCalls++
	if f.adaptConfirmErr != nil {
		return nil, f.adaptConfirmErr
	}
	if f.adaptConfirmedPlan != nil {
		copy := *f.adaptConfirmedPlan
		return &copy, nil
	}
	return &domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		Status:        domain.AdaptationPlanStatusConfirmed,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Brief:         "confirmed",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "Target One",
			SourceChapters: []int{1},
		}},
	}, nil
}

func (f *fakeProjectHost) Export(_ context.Context, opts exp.Options) (*exp.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exportCalls++
	f.exportOptions = opts
	if f.exportErr != nil {
		return nil, f.exportErr
	}
	if f.exportResult != nil {
		return f.exportResult, nil
	}
	return &exp.Result{Path: opts.OutPath, Chapters: 1, Bytes: 12}, nil
}

func (f *fakeProjectHost) ReplayQueue(int64) ([]domain.RuntimeQueueItem, error) {
	return nil, nil
}

func (f *fakeProjectHost) ConfiguredProviders() []string {
	return []string{"openrouter"}
}

func (f *fakeProjectHost) ConfiguredModels(provider string) []string {
	if provider == "openrouter" {
		return []string{"model-a", "model-b"}
	}
	return nil
}

func (f *fakeProjectHost) CurrentModelSelection(role string) (string, string, bool) {
	if role == "" || role == "default" {
		return "openrouter", "model-a", true
	}
	return "openrouter", "model-a", false
}

func (f *fakeProjectHost) SwitchModel(role, provider, model string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switchCalls++
	f.switchRole = role
	f.switchProvider = provider
	f.switchModel = model
	return nil
}

func (f *fakeProjectHost) AddProviderModel(role, providerName string, providerConfig bootstrap.ProviderConfig, model string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addProviderRole = role
	f.addProviderName = providerName
	f.addProviderConfig = providerConfig
	f.addProviderModel = model
	return f.addProviderErr
}

func (f *fakeProjectHost) StartGrokLogin(accountID, accountName string) (grokauth.LoginStart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grokStartAccountID = accountID
	f.grokStartAccountName = accountName
	return f.grokLoginStart, nil
}

func (f *fakeProjectHost) PollGrokLogin() (grokauth.LoginPoll, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grokLoginPoll, nil
}

func (f *fakeProjectHost) CompleteGrokLogin(callbackInput string) (grokauth.AuthStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grokCompleteCallback = callbackInput
	return f.grokCompleteStatus, nil
}

func (f *fakeProjectHost) GrokLoginStatus(accountID string) grokauth.AuthStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grokStatusAccountID = accountID
	return f.grokStatus
}

func (f *fakeProjectHost) CurrentThinking(string) string {
	return "medium"
}

func (f *fakeProjectHost) SetRoleThinking(string, string) error {
	return nil
}

func (f *fakeProjectHost) Events() <-chan host.Event {
	return f.events
}

func (f *fakeProjectHost) Stream() <-chan string {
	return f.stream
}

func (f *fakeProjectHost) Done() <-chan struct{} {
	return f.done
}

func (f *fakeProjectHost) Close() {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	f.closeOnce.Do(func() {
		close(f.events)
		close(f.stream)
		close(f.done)
	})
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
