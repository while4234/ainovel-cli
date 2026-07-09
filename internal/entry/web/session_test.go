package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
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
	_, err = session.ReviseChapter(host.ChapterRevisionRequest{
		Chapter:     1,
		Instruction: "tighten the ending",
		Mode:        host.ChapterRevisionModeRewrite,
	})
	if !errors.Is(err, ErrSessionActionInProgress) {
		t.Fatalf("concurrent revise error = %v, want %v", err, ErrSessionActionInProgress)
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
	if fake.reviseChapterCalls != 0 {
		t.Fatalf("concurrent revise reached host %d time(s)", fake.reviseChapterCalls)
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

func TestProjectSessionAllowsSimulationDuringAdaptationAnalysis(t *testing.T) {
	fake := newFakeProjectHost()
	fake.adaptAnalyzeStarted = make(chan struct{})
	fake.blockAdaptAnalyze = true
	fake.simulateStarted = make(chan struct{})
	fake.blockSimulate = true
	fake.releaseSimulate = make(chan struct{})

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

	simulationErr := make(chan error, 1)
	go func() {
		_, err := session.SimulateFromDir(context.Background(), "simulate")
		simulationErr <- err
	}()
	select {
	case <-fake.simulateStarted:
	case <-time.After(time.Second):
		t.Fatal("simulation analysis did not start during adaptation analysis")
	}

	snap := session.Snapshot()
	if !snap.IsRunning || snap.RuntimeState != "running" || snap.StatusLabel != "RUNNING" {
		t.Fatalf("parallel analysis snapshot should be running, got %+v", snap)
	}
	if !hasRunningActionAgent(snap.Agents, projectActionKindAdaptationAnalysis) {
		t.Fatalf("snapshot should include running adaptation analysis agent: %+v", snap.Agents)
	}
	if !hasRunningActionAgent(snap.Agents, projectActionKindSimulationAnalysis) {
		t.Fatalf("snapshot should include running simulation analysis agent: %+v", snap.Agents)
	}

	if err := session.Continue("keep going"); !errors.Is(err, ErrSessionActionInProgress) {
		t.Fatalf("continue during parallel analyses error = %v, want %v", err, ErrSessionActionInProgress)
	}
	if fake.continueCalls != 0 {
		t.Fatalf("blocked continue reached host %d time(s)", fake.continueCalls)
	}

	close(fake.releaseSimulate)
	select {
	case err := <-simulationErr:
		if err != nil {
			t.Fatalf("simulation returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("simulation analysis did not finish")
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

func TestProjectSessionAllowsExportDuringAdaptationAnalysis(t *testing.T) {
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

	exportPath := filepath.Join(t.TempDir(), "partial.txt")
	if _, err := session.Export(context.Background(), exp.Options{OutPath: exportPath, Format: exp.FormatTXT, Overwrite: true}); err != nil {
		t.Fatalf("export during adaptation analysis returned error: %v", err)
	}
	if fake.exportCalls != 1 {
		t.Fatalf("export host calls = %d, want 1", fake.exportCalls)
	}
	if fake.abortCalls != 0 {
		t.Fatalf("export should not abort writing/analysis, abort calls = %d", fake.abortCalls)
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

func TestProjectSessionBuildAdaptationProposalEmitsLifecycleEvent(t *testing.T) {
	host := newFakeProjectHost()
	session := newTestSessionWithHost("project-1", host)

	_, err := session.BuildAdaptationProposal(adapt.ProposalOptions{
		SourcePath:    "source.txt",
		Granularity:   "free",
		RewritePolicy: "full_rewrite",
		Brief:         "make it bittersweet",
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}

	ev := requireFinishedAdaptProposalEvent(t, session)
	if ev.HostEventID == "" || ev.Event.ID != ev.HostEventID {
		t.Fatalf("proposal event ID mismatch: %+v", ev)
	}
	if ev.Event.Running {
		t.Fatalf("proposal event should be completed: %+v", ev.Event)
	}
	if ev.Event.Category != "ADAPT" || ev.Event.Kind != "proposal" || ev.Event.Level != "success" {
		t.Fatalf("unexpected proposal event metadata: %+v", ev.Event)
	}
	if ev.Event.FinishedAt == nil {
		t.Fatalf("proposal event should include finished_at: %+v", ev.Event)
	}
	if !strings.Contains(ev.Event.Summary, "free") {
		t.Fatalf("summary should include mode, got %q", ev.Event.Summary)
	}
}

func TestProjectSessionSimulationRetryEventDoesNotFailRun(t *testing.T) {
	session := newTestSessionWithHost("project-1", newFakeProjectHost())
	events := make(chan sim.Event, 2)
	events <- sim.Event{
		Stage:   sim.StageAnalyze,
		Current: 25,
		Total:   47,
		Message: "模型调用重试 2/7：provider gateway error: 503 Service Unavailable",
		Err:     errors.New("provider gateway error: 503 Service Unavailable"),
	}
	events <- sim.Event{Stage: sim.StageDone, Current: 47, Total: 47, Message: "simulation complete"}
	close(events)

	out, err := session.consumeSimulationEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("consumeSimulationEvents: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("events = %+v, want retry plus done", out)
	}
	retry := requireHostEventSummary(t, session.HistoryAfter(0), "模型调用重试 2/7：provider gateway error: 503 Service Unavailable")
	if retry.Event.Level != "warn" || retry.Event.Kind != string(sim.StageAnalyze) {
		t.Fatalf("retry event should be non-terminal warn: %+v", retry.Event)
	}
	if !strings.Contains(retry.Event.Detail, "503 Service Unavailable") {
		t.Fatalf("retry detail = %q, want 503 detail", retry.Event.Detail)
	}
}

func TestProjectSessionBuildAdaptationProposalAppendsProgressEvents(t *testing.T) {
	host := newFakeProjectHost()
	session := newTestSessionWithHost("project-1", host)

	_, err := session.BuildAdaptationProposal(adapt.ProposalOptions{
		SourcePath:    "source.txt",
		Granularity:   "free",
		RewritePolicy: "full_rewrite",
		Brief:         "make progress visible",
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}

	history := session.HistoryAfter(0)
	progress := requireHostEventSummary(t, history, "test proposal progress")
	if progress.HostEventID != "" || progress.Event.Category != "ADAPT" || progress.Event.Kind != string(adapt.StagePlan) || progress.Event.Level != "info" {
		t.Fatalf("unexpected proposal progress event: %+v", progress)
	}
	warn := requireHostEventSummary(t, history, "test proposal repair")
	if warn.HostEventID != "" || warn.Event.Level != "warn" || !strings.Contains(warn.Event.Detail, "missing chapter") {
		t.Fatalf("recoverable proposal progress should be warn with detail: %+v", warn)
	}
}

func TestProjectSessionReviseAdaptationProposalAppendsProgressEvents(t *testing.T) {
	host := newFakeProjectHost()
	session := newTestSessionWithHost("project-1", host)

	_, err := session.ReviseAdaptationProposalContext(context.Background(), adapt.ProposalRevisionOptions{
		FromChapter: 1,
		Instruction: "tighten the opening",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationProposalContext: %v", err)
	}

	progress := requireHostEventSummary(t, session.HistoryAfter(0), "test revision progress")
	if progress.HostEventID != "" || progress.Event.Category != "ADAPT" || progress.Event.Kind != string(adapt.StagePlan) || progress.Event.Level != "info" {
		t.Fatalf("unexpected revision progress event: %+v", progress)
	}
}

func TestProjectSessionBuildAdaptationProposalEmitsFailedLifecycleEvent(t *testing.T) {
	host := newFakeProjectHost()
	host.adaptProposalErr = errors.New("planner timeout")
	session := newTestSessionWithHost("project-1", host)

	_, err := session.BuildAdaptationProposal(adapt.ProposalOptions{
		SourcePath:  "source.txt",
		Granularity: "arc",
		Brief:       "focus on emotional tension",
	})
	if !errors.Is(err, host.adaptProposalErr) {
		t.Fatalf("BuildAdaptationProposal error = %v, want %v", err, host.adaptProposalErr)
	}

	ev := requireFinishedAdaptProposalEvent(t, session)
	if !ev.Event.Failed || ev.Event.Level != "error" {
		t.Fatalf("proposal event should be failed error: %+v", ev.Event)
	}
	if ev.Event.Running || ev.Event.FinishedAt == nil {
		t.Fatalf("failed proposal event should be completed: %+v", ev.Event)
	}
	if !strings.Contains(ev.Event.Detail, "planner timeout") {
		t.Fatalf("failed proposal detail = %q", ev.Event.Detail)
	}
	history := session.HistoryAfter(0)
	if len(history) == 0 || history[len(history)-1].Type != webEventTypeSnapshot {
		t.Fatalf("failed proposal should publish a final snapshot event: %+v", history)
	}
}

func TestProjectSessionBuildAdaptationProposalDoesNotAddTotalDeadline(t *testing.T) {
	fake := newFakeProjectHost()
	session := newTestSessionWithHost("project-1", fake)

	_, err := session.BuildAdaptationProposalContext(context.Background(), adapt.ProposalOptions{
		SourcePath:  "source.txt",
		Granularity: "arc",
		Brief:       "generate a long staged proposal",
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalContext: %v", err)
	}
	if fake.adaptProposalContextHadDeadline {
		t.Fatal("proposal generation should not add a fixed total deadline")
	}
}

func TestProjectSessionBuildAdaptationProposalShowsRunningAndCancels(t *testing.T) {
	fake := newFakeProjectHost()
	fake.snapshot = hostpkgSnapshotIdle()
	fake.adaptProposalStarted = make(chan struct{})
	fake.blockAdaptProposal = true
	session := newTestSessionWithHost("project-1", fake)

	proposalErr := make(chan error, 1)
	go func() {
		_, err := session.BuildAdaptationProposalContext(context.Background(), adapt.ProposalOptions{
			SourcePath:  "source.txt",
			Granularity: "free",
			Brief:       "expand into a long safe outline",
		})
		proposalErr <- err
	}()

	select {
	case <-fake.adaptProposalStarted:
	case <-time.After(time.Second):
		t.Fatal("adaptation proposal generation did not start")
	}

	snap := session.Snapshot()
	if !snap.IsRunning || snap.RuntimeState != "running" || snap.StatusLabel != "RUNNING" {
		t.Fatalf("proposal snapshot should be running, got %+v", snap)
	}
	if !hasRunningActionAgent(snap.Agents, projectActionKindAdaptationProposal) {
		t.Fatalf("proposal snapshot should include running proposal agent: %+v", snap.Agents)
	}

	if !session.Pause() {
		t.Fatal("Pause should report a canceled proposal action")
	}

	select {
	case err := <-proposalErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("proposal error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("adaptation proposal did not stop after pause")
	}

	snap = session.Snapshot()
	if snap.IsRunning || snap.RuntimeState == "running" || snap.StatusLabel == "RUNNING" {
		t.Fatalf("proposal snapshot should be idle after cancellation, got %+v", snap)
	}
	ev := requireFinishedAdaptProposalEvent(t, session)
	if !ev.Event.Failed || ev.Event.Level != "error" || !strings.Contains(ev.Event.Detail, "改编提案生成已取消") {
		t.Fatalf("proposal cancel should finish as a visible failed event: %+v", ev.Event)
	}
	history := session.HistoryAfter(0)
	if len(history) == 0 || history[len(history)-1].Type != webEventTypeSnapshot {
		t.Fatalf("proposal cancel should publish a final idle snapshot: %+v", history)
	}
	finalSnap, ok := history[len(history)-1].Snapshot.(host.UISnapshot)
	if !ok {
		t.Fatalf("final snapshot type = %T, want host.UISnapshot", history[len(history)-1].Snapshot)
	}
	if finalSnap.IsRunning {
		t.Fatalf("proposal cancel final snapshot should be idle: %+v", finalSnap)
	}
}

func TestProjectSessionAdaptCoCreateCommitCanBeCanceledFromCoCreateCancel(t *testing.T) {
	fake := newFakeProjectHost()
	fake.snapshot = hostpkgSnapshotIdle()
	fake.adaptProposalStarted = make(chan struct{})
	fake.blockAdaptProposal = true
	session := newTestSessionWithHost("project-1", fake)
	draft := strings.Join([]string{
		"## Adapt Mode",
		"granularity=arc",
		"rewrite_policy=full_rewrite",
		"word_tolerance=disabled",
		"",
		"## Core Goal",
		"- Preserve the mainline and strengthen the heroine arc.",
	}, "\n")
	session.cocreate = &webCoCreateSession{
		kind: webCoCreateKindAdapt,
		session: startup.NewCoCreateSessionFromSnapshot(startup.CoCreateSnapshot{
			History: []host.CoCreateMessage{
				{Role: "user", Content: "prepare adaptation brief"},
				{Role: "assistant", Content: "<reply>ready</reply><draft>" + draft + "</draft><ready>true</ready><suggestions></suggestions>"},
			},
			DraftPrompt:     draft,
			DraftHistoryLen: 2,
			Ready:           true,
		}),
		sourcePath:         "source.txt",
		sourceFile:         "source.txt",
		adaptGranularity:   domain.AdaptationGranularityArc,
		adaptRewritePolicy: domain.AdaptationRewriteFullRewrite,
		adaptWordTolerance: 0,
		draftConsolidated:  true,
	}

	commitErr := make(chan error, 1)
	go func() {
		_, err := session.CommitCoCreate(context.Background())
		commitErr <- err
	}()

	select {
	case <-fake.adaptProposalStarted:
	case <-time.After(time.Second):
		t.Fatal("adapt co-create proposal generation did not start")
	}

	state, err := session.CancelCoCreate()
	if err != nil {
		t.Fatalf("CancelCoCreate should cancel running proposal, got %v", err)
	}
	if state.Active {
		t.Fatalf("CancelCoCreate should close the co-create session after canceling proposal")
	}
	if session.cocreate != nil {
		t.Fatalf("session co-create state should be cleared after cancellation")
	}
	select {
	case err := <-commitErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("commit error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("adapt co-create commit did not stop after cancel")
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

func TestProjectSessionEventHistoryHonorsAfterWithoutAppendingSnapshot(t *testing.T) {
	session, err := NewProjectSession(ProjectManifest{ID: "project-1"}, newFakeProjectHost())
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	old := session.appendStreamDelta("old delta")
	newer := session.appendStreamDelta("new delta")
	beforeLatest := session.EventHistory(0).LatestSeq

	history := session.EventHistory(old.Seq)
	if history.ProjectID != "project-1" {
		t.Fatalf("history project id = %q", history.ProjectID)
	}
	if history.LatestSeq != beforeLatest || history.LatestSeq != newer.Seq {
		t.Fatalf("history should not append events while reading: before=%d after=%d newer=%d", beforeLatest, history.LatestSeq, newer.Seq)
	}
	if history.HistoryLimit != webEventHistoryLimit {
		t.Fatalf("history limit = %d, want %d", history.HistoryLimit, webEventHistoryLimit)
	}
	if len(history.Events) != 1 || history.Events[0].Seq != newer.Seq {
		t.Fatalf("history after %d = %+v, want only seq %d", old.Seq, history.Events, newer.Seq)
	}
	for _, ev := range history.Events {
		if ev.Type == webEventTypeSnapshot {
			t.Fatalf("event history should not append snapshot events: %+v", history.Events)
		}
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

func TestProjectSessionPublishesCoCreateHostEventsByKind(t *testing.T) {
	cases := []struct {
		name string
		kind string
		req  webCoCreateBeginRequest
	}{
		{
			name: "normal",
			kind: webCoCreateKindNormal,
			req: webCoCreateBeginRequest{
				Kind:    webCoCreateKindNormal,
				Initial: "write a moon mystery",
			},
		},
		{
			name: "stage",
			kind: webCoCreateKindStage,
			req: webCoCreateBeginRequest{
				Kind: webCoCreateKindStage,
			},
		},
		{
			name: "adapt",
			kind: webCoCreateKindAdapt,
			req: webCoCreateBeginRequest{
				Kind: webCoCreateKindAdapt,
				Mode: domain.AdaptationGranularityFree,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeProjectHost()
			reply := host.CoCreateReply{
				Message: "confirmed",
				Prompt:  "## plan\n- keep going",
				Ready:   false,
				Raw:     "<reply>confirmed</reply><draft>## plan\n- keep going</draft><ready>false</ready><suggestions></suggestions>",
			}
			fake.cocreateReply = reply
			fake.stageCoCreateReply = reply
			fake.adaptCoCreateReply = reply
			session, err := NewProjectSession(ProjectManifest{ID: "project-1"}, fake)
			if err != nil {
				t.Fatalf("NewProjectSession: %v", err)
			}
			defer session.Close()

			if _, err := session.BeginCoCreate(context.Background(), tc.req); err != nil {
				t.Fatalf("BeginCoCreate: %v", err)
			}

			var got *APIHostEvent
			for _, ev := range session.HistoryAfter(0) {
				if ev.Type == webEventTypeHostEvent && ev.Event != nil && ev.Event.Category == "COCREATE" {
					got = ev.Event
				}
			}
			if got == nil {
				t.Fatalf("missing COCREATE host event in history")
			}
			if got.Kind != tc.kind {
				t.Fatalf("event kind = %q, want %q", got.Kind, tc.kind)
			}
			if got.Running || got.Failed || got.Level == "error" {
				t.Fatalf("event should be completed successfully, got %+v", got)
			}
			if strings.TrimSpace(got.Summary) == "" {
				t.Fatalf("event summary should not be empty: %+v", got)
			}
		})
	}
}

func TestProjectSessionBeginCoCreateCanRetryAfterFailure(t *testing.T) {
	fake := newFakeProjectHost()
	fake.cocreateErr = errors.New("upstream unavailable")
	session, err := NewProjectSession(ProjectManifest{ID: "project-1"}, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	if _, err := session.BeginCoCreate(context.Background(), webCoCreateBeginRequest{
		Kind:    webCoCreateKindNormal,
		Initial: "first brief",
	}); err == nil {
		t.Fatal("BeginCoCreate should fail")
	}

	fake.cocreateErr = nil
	fake.cocreateReply = host.CoCreateReply{
		Message: "ok",
		Prompt:  "## plan\n- continue",
		Ready:   false,
		Raw:     "<reply>ok</reply><draft>## plan\n- continue</draft><ready>false</ready><suggestions></suggestions>",
	}
	state, err := session.BeginCoCreate(context.Background(), webCoCreateBeginRequest{
		Kind:    webCoCreateKindNormal,
		Initial: "retry brief",
	})
	if err != nil {
		t.Fatalf("BeginCoCreate retry: %v", err)
	}
	if fake.cocreateCalls != 2 {
		t.Fatalf("co-create calls = %d, want 2", fake.cocreateCalls)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("message count = %d, want 2: %+v", len(state.Messages), state.Messages)
	}
	if state.Messages[0].Content != "retry brief" {
		t.Fatalf("retry should replace failed history, got first message %q", state.Messages[0].Content)
	}
	for _, message := range state.Messages {
		if message.Content == "first brief" {
			t.Fatalf("failed history leaked into retry: %+v", state.Messages)
		}
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

	req = httptest.NewRequest(http.MethodGet, "/api/projects/project-1/events/history?after=abc", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid history after status = %d body=%s", rec.Code, rec.Body.String())
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
		actionKinds: make(map[string]int),
		hostEventAt: make(map[string]int),
		subscribers: make(map[chan WebEvent]struct{}),
	}
}

func newTestSessionWithHost(projectID string, h projectHost) *ProjectSession {
	session := newTestSessionWithoutHost(projectID)
	session.host = h
	return session
}

func requireFinishedAdaptProposalEvent(t *testing.T, session *ProjectSession) WebEvent {
	t.Helper()
	var proposalEvents []WebEvent
	var finished *WebEvent
	var sawPlannerRequest bool
	for _, ev := range session.HistoryAfter(0) {
		if ev.Type == webEventTypeHostEvent &&
			ev.Event != nil &&
			ev.Event.Category == "ADAPT" &&
			ev.Event.Kind == "proposal" {
			proposalEvents = append(proposalEvents, ev)
			if ev.Event.FinishedAt != nil {
				copy := ev
				finished = &copy
			}
			if strings.Contains(ev.Event.Detail, "planner request") {
				sawPlannerRequest = true
			}
		}
	}
	if len(proposalEvents) != 2 {
		t.Fatalf("proposal event count = %d, want 2: %+v", len(proposalEvents), proposalEvents)
	}
	if !sawPlannerRequest {
		t.Fatalf("proposal events should include planner request progress: %+v", proposalEvents)
	}
	if finished == nil {
		t.Fatalf("proposal events should include finished lifecycle event: %+v", proposalEvents)
	}
	return *finished
}

func requireHostEventSummary(t *testing.T, history []WebEvent, summary string) WebEvent {
	t.Helper()
	for _, ev := range history {
		if ev.Type == webEventTypeHostEvent && ev.Event != nil && ev.Event.Summary == summary {
			return ev
		}
	}
	t.Fatalf("host event summary %q not found in history: %+v", summary, history)
	return WebEvent{}
}

func hostpkgSnapshotIdle() host.UISnapshot {
	return host.UISnapshot{
		RuntimeState: "idle",
		StatusLabel:  "READY",
	}
}

func hasRunningActionAgent(agents []host.AgentSnapshot, kind string) bool {
	for _, agent := range agents {
		if agent.State == "running" && agent.TaskKind == kind {
			return true
		}
	}
	return false
}

type fakeProjectHost struct {
	mu sync.Mutex

	snapshot host.UISnapshot

	resumeStarted              chan struct{}
	resumeStartedOnce          sync.Once
	releaseResume              chan struct{}
	resumeErr                  error
	reviseChapterErr           error
	continueErr                error
	steerErr                   error
	simulateErr                error
	importErr                  error
	importNovelErr             error
	adaptAnalyzeErr            error
	adaptProposalErr           error
	adaptBriefingErr           error
	adaptConfirmErr            error
	adaptStartErr              error
	exportErr                  error
	rollbackPreviewErr         error
	rollbackErr                error
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
	blockAdaptProposal         bool
	blockSimulate              bool

	resumeCalls                     int
	reviseChapterCalls              int
	continueCalls                   int
	steerCalls                      int
	simulateCalls                   int
	importCalls                     int
	importNovelCalls                int
	adaptAnalyzeCalls               int
	adaptProposalCalls              int
	adaptBriefingCalls              int
	resolveAdaptDecisionCalls       int
	adaptConfirmCalls               int
	adaptStartCalls                 int
	exportCalls                     int
	rollbackPreviewCalls            int
	rollbackCalls                   int
	abortCalls                      int
	prepareRulesCalls               int
	prepareExternalRulesCalls       int
	setWordBudgetCalls              int
	startPreparedCalls              int
	cocreateCalls                   int
	stageCoCreateCalls              int
	adaptCoCreateCalls              int
	pauseCoCreateCalls              int
	resumeCoCreateCalls             int
	cancelCoCreateCalls             int
	closeCalls                      int
	simulateDir                     string
	importPath                      string
	importNovelPath                 string
	importNovelResumeFrom           int
	adaptAnalyzeStarted             chan struct{}
	adaptAnalyzeBeforeDone          func(string)
	adaptAnalyzePrefixEvents        []adapt.Event
	adaptProposalStarted            chan struct{}
	adaptProposalContextHadDeadline bool
	adaptBriefingStarted            chan struct{}
	releaseAdaptBriefing            chan struct{}
	simulateStarted                 chan struct{}
	releaseSimulate                 chan struct{}
	adaptSourcePath                 string
	adaptProposalOptions            adapt.ProposalOptions
	adaptRevisionOptions            adapt.ProposalRevisionOptions
	adaptProposal                   *domain.AdaptationPlan
	adaptBriefing                   *domain.AdaptationCoCreateBriefing
	lastAdaptBriefingSource         string
	lastAdaptBriefingIntent         domain.AdaptationCoCreateIntent
	adaptRevisionProposal           *domain.AdaptationPlan
	adaptConfirmedPlan              *domain.AdaptationPlan
	adaptOptions                    adapt.ProposalOptions
	exportOptions                   exp.Options
	rollbackPreview                 domain.RollbackPreview
	rollbackResult                  domain.RollbackResult
	addProviderRole                 string
	configureProviderRole           string
	configureOriginalProvider       string
	addProviderName                 string
	configureProviderName           string
	addProviderConfig               bootstrap.ProviderConfig
	configureProviderConfig         bootstrap.ProviderConfig
	addProviderModel                string
	configureProviderModel          string
	configureNetworkAttempts        int
	configureAutoSwitchPool         bool
	removeProviderName              string
	removeProviderModel             string
	switchRole                      string
	switchProvider                  string
	switchModel                     string
	grokStartAccountID              string
	grokStartAccountName            string
	grokCompleteCallback            string
	grokStatusAccountID             string
	preparedRulesPrompt             string
	preparedExternalRulesPrompt     string
	wordBudget                      *domain.WordBudget
	startPreparedPrompt             string
	reviseChapterRequest            host.ChapterRevisionRequest
	reviseChapterResult             host.ChapterRevisionResult
	resumeCoCreateDraft             string
	lastCoCreateHistory             []host.CoCreateMessage
	adaptCoCreateHistories          [][]host.CoCreateMessage
	cocreateReply                   host.CoCreateReply
	stageCoCreateReply              host.CoCreateReply
	adaptCoCreateReply              host.CoCreateReply
	cocreateReplies                 []host.CoCreateReply
	stageCoCreateReplies            []host.CoCreateReply
	adaptCoCreateReplies            []host.CoCreateReply
	cocreateProgress                []coCreateProgressStep
	pauseCoCreateOK                 bool
	abortOK                         bool
	exportResult                    *exp.Result
	addProviderErr                  error
	removeProviderErr               error
	setCoCreateTimeoutErr           error
	setCoCreateMaxTokensErr         error
	setRetrySettingsErr             error
	switchCalls                     int
	clearModelRouteCalls            int
	removeProviderCalls             int
	setCoCreateTimeoutCalls         int
	setCoCreateMaxTokensCalls       int
	setRetrySettingsCalls           int
	coCreateTimeoutSeconds          int
	coCreateMaxTokens               int
	modelCallMaxAttempts            int
	structureRepairMaxAttempts      int
	budgetQualityMaxAttempts        int
	clearModelRouteRole             string
	grokLoginStart                  grokauth.LoginStart
	grokLoginPoll                   grokauth.LoginPoll
	grokCompleteStatus              grokauth.AuthStatus
	grokStatus                      grokauth.AuthStatus
	adaptRevisionCalls              int

	events    chan host.Event
	stream    chan string
	done      chan struct{}
	closeOnce sync.Once
}

type coCreateProgressStep struct {
	kind string
	text string
}

func popCoCreateReply(queue *[]host.CoCreateReply, fallback host.CoCreateReply) host.CoCreateReply {
	if queue == nil || len(*queue) == 0 {
		return fallback
	}
	reply := (*queue)[0]
	*queue = (*queue)[1:]
	return reply
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

func (f *fakeProjectHost) RollbackPreview() (domain.RollbackPreview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbackPreviewCalls++
	if f.rollbackPreviewErr != nil {
		return domain.RollbackPreview{}, f.rollbackPreviewErr
	}
	if f.rollbackPreview.PreviewHash == "" {
		f.rollbackPreview = domain.RollbackPreviewWithHash(f.rollbackPreview)
	}
	return f.rollbackPreview, nil
}

func (f *fakeProjectHost) Rollback(req domain.RollbackRequest) (domain.RollbackResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbackCalls++
	if f.rollbackErr != nil {
		return domain.RollbackResult{}, f.rollbackErr
	}
	if f.rollbackResult.Preview.PreviewHash == "" {
		preview := f.rollbackPreview
		if preview.PreviewHash == "" {
			preview = domain.RollbackPreviewWithHash(preview)
		}
		f.rollbackResult.Preview = preview
	}
	return f.rollbackResult, nil
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

func (f *fakeProjectHost) ReviseChapter(req host.ChapterRevisionRequest) (host.ChapterRevisionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviseChapterCalls++
	f.reviseChapterRequest = req
	if f.reviseChapterErr != nil {
		return host.ChapterRevisionResult{}, f.reviseChapterErr
	}
	if f.reviseChapterResult.Chapter > 0 {
		return f.reviseChapterResult, nil
	}
	return host.ChapterRevisionResult{
		Chapter:         req.Chapter,
		Instruction:     req.Instruction,
		Mode:            req.Mode,
		Label:           "resume test label",
		PendingRewrites: []int{req.Chapter},
		StaleNotice:     "stale test notice",
	}, nil
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
	reply := popCoCreateReply(&f.cocreateReplies, f.cocreateReply)
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
	reply := popCoCreateReply(&f.stageCoCreateReplies, f.stageCoCreateReply)
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
	f.adaptCoCreateHistories = append(f.adaptCoCreateHistories, append([]host.CoCreateMessage(nil), history...))
	reply := popCoCreateReply(&f.adaptCoCreateReplies, f.adaptCoCreateReply)
	err := f.adaptCoCreateErr
	progress := append([]coCreateProgressStep(nil), f.cocreateProgress...)
	f.mu.Unlock()
	emitCoCreateProgress(progress, onProgress)
	return reply, err
}

func (f *fakeProjectHost) EnsureAdaptationCoCreateBriefing(_ context.Context, sourcePath string, intent domain.AdaptationCoCreateIntent) (*domain.AdaptationCoCreateBriefing, error) {
	f.mu.Lock()
	f.adaptBriefingCalls++
	f.lastAdaptBriefingSource = sourcePath
	f.lastAdaptBriefingIntent = intent
	started := f.adaptBriefingStarted
	release := f.releaseAdaptBriefing
	if started != nil {
		f.adaptBriefingStarted = nil
	}
	f.mu.Unlock()

	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.adaptBriefing != nil {
		briefing := *f.adaptBriefing
		briefing.IntentHash = intent.IntentHash
		f.adaptBriefing = &briefing
	}
	return f.adaptBriefing, f.adaptBriefingErr
}

func (f *fakeProjectHost) ResolveAdaptationCoCreateDecision(decisionID, optionID, customAnswer string) (*domain.AdaptationCoCreateBriefing, error) {
	return f.ResolveAdaptationCoCreateDecisions([]domain.AdaptationResolvedDecision{{
		DecisionID:   decisionID,
		OptionID:     optionID,
		CustomAnswer: customAnswer,
	}})
}

func (f *fakeProjectHost) ResolveAdaptationCoCreateDecisions(decisions []domain.AdaptationResolvedDecision) (*domain.AdaptationCoCreateBriefing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveAdaptDecisionCalls += len(decisions)
	if f.adaptBriefing == nil {
		return nil, fmt.Errorf("co-create briefing is required")
	}
	for _, item := range decisions {
		resolved := domain.AdaptationResolvedDecision{
			DecisionID:   strings.TrimSpace(item.DecisionID),
			OptionID:     strings.TrimSpace(item.OptionID),
			CustomAnswer: strings.TrimSpace(item.CustomAnswer),
		}
		replaced := false
		for i := range f.adaptBriefing.ResolvedDecisions {
			if f.adaptBriefing.ResolvedDecisions[i].DecisionID == resolved.DecisionID {
				f.adaptBriefing.ResolvedDecisions[i] = resolved
				replaced = true
				break
			}
		}
		if !replaced {
			f.adaptBriefing.ResolvedDecisions = append(f.adaptBriefing.ResolvedDecisions, resolved)
		}
		for i := range f.adaptBriefing.Decisions {
			if f.adaptBriefing.Decisions[i].ID == resolved.DecisionID {
				f.adaptBriefing.Decisions[i].Status = "resolved"
			}
		}
	}
	return f.adaptBriefing, nil
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
	started := f.simulateStarted
	block := f.blockSimulate
	release := f.releaseSimulate
	if started != nil {
		f.simulateStarted = nil
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if started != nil {
		close(started)
	}
	if block {
		if release != nil {
			<-release
		} else {
			return make(chan sim.Event), nil
		}
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
	beforeDone := f.adaptAnalyzeBeforeDone
	prefixEvents := append([]adapt.Event(nil), f.adaptAnalyzePrefixEvents...)
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
	if beforeDone != nil {
		beforeDone(sourcePath)
	}
	events := make(chan adapt.Event, len(prefixEvents)+2)
	for _, ev := range prefixEvents {
		events <- ev
	}
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

func (f *fakeProjectHost) BuildAdaptationProposalContext(ctx context.Context, options adapt.ProposalOptions) (*domain.AdaptationPlan, error) {
	f.mu.Lock()
	f.adaptProposalCalls++
	f.adaptProposalOptions = options
	_, f.adaptProposalContextHadDeadline = ctx.Deadline()
	err := f.adaptProposalErr
	proposal := f.adaptProposal
	emit := options.EmitProgress
	started := f.adaptProposalStarted
	block := f.blockAdaptProposal
	requireAnalyzed := f.requireAnalyzedAdaptSource
	analyzedSource := f.adaptSourcePath
	if started != nil {
		f.adaptProposalStarted = nil
	}
	f.mu.Unlock()

	if started != nil {
		close(started)
	}
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if requireAnalyzed && options.SourcePath != analyzedSource {
		return nil, fmt.Errorf("adaptation source %q has not completed analysis", options.SourcePath)
	}
	if err != nil {
		return nil, err
	}
	if emit != nil {
		emit(adapt.StagePlan, 1, 3, "test proposal progress", nil)
		emit(adapt.StagePlan, 2, 3, "test proposal repair", errors.New("missing chapter"))
	}
	if proposal != nil {
		copy := *proposal
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

func (f *fakeProjectHost) BuildAdaptationProposalVolumesContext(ctx context.Context, options adapt.ProposalOptions) (*adapt.ProposalStageResult, error) {
	proposal, err := f.BuildAdaptationProposalContext(ctx, options)
	if err != nil {
		return nil, err
	}
	if proposal != nil && proposal.Status == domain.AdaptationPlanStatusVolumeReview {
		return &adapt.ProposalStageResult{
			VolumeReview: &domain.AdaptationVolumeReview{
				Status:             domain.AdaptationPlanStatusVolumeReview,
				Brief:              proposal.Brief,
				Granularity:        proposal.Granularity,
				RewritePolicy:      proposal.RewritePolicy,
				WordTolerance:      proposal.WordTolerance,
				TargetChapterCount: lastAdaptationVolumeChapter(proposal.Volumes),
				MainlineRules:      append([]string(nil), proposal.MainlineRules...),
				RelationshipGoals:  append([]string(nil), proposal.RelationshipGoals...),
				Volumes:            append([]domain.AdaptationVolumePlan(nil), proposal.Volumes...),
			},
		}, nil
	}
	return &adapt.ProposalStageResult{Proposal: proposal}, nil
}

func lastAdaptationVolumeChapter(volumes []domain.AdaptationVolumePlan) int {
	last := 0
	for _, volume := range volumes {
		if volume.TargetTo > last {
			last = volume.TargetTo
		}
	}
	return last
}

func (f *fakeProjectHost) ReviseAdaptationProposalContext(_ context.Context, options adapt.ProposalRevisionOptions) (*domain.AdaptationPlan, error) {
	f.mu.Lock()
	f.adaptRevisionCalls++
	f.adaptRevisionOptions = options
	emit := options.EmitProgress
	if f.adaptProposalErr != nil {
		f.mu.Unlock()
		return nil, f.adaptProposalErr
	}
	if f.adaptRevisionProposal != nil {
		copy := *f.adaptRevisionProposal
		f.mu.Unlock()
		if emit != nil {
			emit(adapt.StagePlan, 1, 1, "test revision progress", nil)
		}
		return &copy, nil
	}
	f.mu.Unlock()
	if emit != nil {
		emit(adapt.StagePlan, 1, 1, "test revision progress", nil)
	}
	return &domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityFree,
		Status:        domain.AdaptationPlanStatusProposal,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Brief:         "revised",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "Revised Target One",
			SourceChapters: []int{1},
		}},
	}, nil
}

func (f *fakeProjectHost) ReviseAdaptationVolumeReviewContext(_ context.Context, options adapt.ProposalRevisionOptions) (*domain.AdaptationVolumeReview, error) {
	f.mu.Lock()
	f.adaptRevisionCalls++
	f.adaptRevisionOptions = options
	emit := options.EmitProgress
	err := f.adaptProposalErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if emit != nil {
		emit(adapt.StagePlan, 1, 1, "test volume review revision progress", nil)
	}
	return &domain.AdaptationVolumeReview{
		Status:             domain.AdaptationPlanStatusVolumeReview,
		Granularity:        domain.AdaptationGranularityFree,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		Brief:              "revised volume review",
		TargetChapterCount: 1,
		Volumes: []domain.AdaptationVolumePlan{{
			Index:      1,
			Title:      "Revised Volume",
			TargetFrom: 1,
			TargetTo:   1,
			SourceFrom: 1,
			SourceTo:   1,
		}},
	}, nil
}

func (f *fakeProjectHost) BuildAdaptationProposalDetailsContext(_ context.Context, options adapt.ProposalDetailsOptions) (*domain.AdaptationPlan, error) {
	f.mu.Lock()
	err := f.adaptProposalErr
	proposal := f.adaptProposal
	emit := options.EmitProgress
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if emit != nil {
		emit(adapt.StagePlan, 1, 1, "test details progress", nil)
	}
	if proposal != nil && proposal.Status != domain.AdaptationPlanStatusVolumeReview {
		copy := *proposal
		return &copy, nil
	}
	return &domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityFree,
		Status:        domain.AdaptationPlanStatusProposal,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Brief:         "details",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "Detailed Target One",
			SourceChapters: []int{1},
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
	if opts.OutPath != "" {
		if err := os.MkdirAll(filepath.Dir(opts.OutPath), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(opts.OutPath, []byte("fake export"), 0o644); err != nil {
			return nil, err
		}
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

func (f *fakeProjectHost) ProviderConfig(provider string) (bootstrap.ProviderConfig, bool) {
	if provider != "openrouter" {
		return bootstrap.ProviderConfig{}, false
	}
	return bootstrap.ProviderConfig{
		Type:    "openai",
		API:     "chat",
		BaseURL: "https://openrouter.ai/api/v1",
		Models:  []string{"model-a", "model-b"},
	}, true
}

func (f *fakeProjectHost) ModelAutoSwitchConfig() bootstrap.ModelAutoSwitchConfig {
	enabled := true
	return bootstrap.ModelAutoSwitchConfig{
		Enabled:            &enabled,
		FallbackBackends:   []string{"openrouter"},
		NetworkMaxAttempts: 4,
	}
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

func (f *fakeProjectHost) ClearModelRoute(role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearModelRouteCalls++
	f.clearModelRouteRole = role
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

func (f *fakeProjectHost) ConfigureProviderModel(_ context.Context, update host.ProviderModelUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configureProviderRole = update.Role
	f.configureOriginalProvider = update.OriginalProvider
	f.configureProviderName = update.Provider
	f.configureProviderConfig = update.ProviderConfig
	f.configureProviderModel = update.Model
	f.configureNetworkAttempts = update.NetworkMaxAttempts
	f.configureAutoSwitchPool = update.AutoSwitchCandidatePool
	return f.addProviderErr
}

func (f *fakeProjectHost) SyncInheritedProviderFromGlobal(bootstrap.Config, string, string) error {
	return nil
}

func (f *fakeProjectHost) SyncModelSettingsFromGlobal(bootstrap.Config) error {
	return nil
}

func (f *fakeProjectHost) TestProviderModel(_ context.Context, _ string, providerName string, _ bootstrap.ProviderConfig, model string) (host.ProviderModelTestResult, error) {
	return host.ProviderModelTestResult{
		Provider: providerName,
		Model:    model,
		Status:   "ok",
	}, nil
}

func (f *fakeProjectHost) TestConfiguredProviderModel(_ context.Context, update host.ProviderModelUpdate) (host.ProviderModelTestResult, error) {
	return host.ProviderModelTestResult{
		Provider: update.Provider,
		Model:    update.Model,
		Status:   "ok",
	}, nil
}

func (f *fakeProjectHost) DiscoverProviderModels(_ context.Context, providerName string, _ bootstrap.ProviderConfig, _ string) (host.ProviderModelDiscoveryResult, error) {
	return host.ProviderModelDiscoveryResult{
		Provider:  providerName,
		Models:    []string{"model-a", "model-b"},
		Supported: true,
		Status:    "ok",
	}, nil
}

func (f *fakeProjectHost) DiscoverConfiguredProviderModels(_ context.Context, update host.ProviderModelUpdate) (host.ProviderModelDiscoveryResult, error) {
	return host.ProviderModelDiscoveryResult{
		Provider:  update.Provider,
		Models:    []string{"model-a", "model-b"},
		Supported: true,
		Status:    "ok",
	}, nil
}

func (f *fakeProjectHost) RemoveProviderModel(providerName, model string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeProviderCalls++
	f.removeProviderName = providerName
	f.removeProviderModel = model
	return f.removeProviderErr
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

func (f *fakeProjectHost) CurrentCoCreateTimeoutSeconds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.coCreateTimeoutSeconds > 0 {
		return f.coCreateTimeoutSeconds
	}
	return bootstrap.DefaultCoCreateTimeoutSeconds
}

func (f *fakeProjectHost) SetCoCreateTimeoutSeconds(seconds int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCoCreateTimeoutCalls++
	f.coCreateTimeoutSeconds = seconds
	return f.setCoCreateTimeoutErr
}

func (f *fakeProjectHost) CurrentCoCreateMaxTokens() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.coCreateMaxTokens > 0 {
		return f.coCreateMaxTokens
	}
	return bootstrap.DefaultCoCreateMaxTokens
}

func (f *fakeProjectHost) SetCoCreateMaxTokens(tokens int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCoCreateMaxTokensCalls++
	f.coCreateMaxTokens = tokens
	return f.setCoCreateMaxTokensErr
}

func (f *fakeProjectHost) CurrentStructureRepairMaxAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.structureRepairMaxAttempts > 0 {
		return f.structureRepairMaxAttempts
	}
	return bootstrap.DefaultStructureRepairMaxAttempts
}

func (f *fakeProjectHost) CurrentBudgetQualityMaxAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.budgetQualityMaxAttempts > 0 {
		return f.budgetQualityMaxAttempts
	}
	return bootstrap.DefaultBudgetQualityMaxAttempts
}

func (f *fakeProjectHost) SetRetrySettings(modelCallMaxAttempts, structureRepairMaxAttempts, budgetQualityMaxAttempts int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setRetrySettingsCalls++
	f.modelCallMaxAttempts = modelCallMaxAttempts
	f.structureRepairMaxAttempts = structureRepairMaxAttempts
	f.budgetQualityMaxAttempts = budgetQualityMaxAttempts
	return f.setRetrySettingsErr
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
