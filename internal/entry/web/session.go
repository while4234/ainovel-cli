package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
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

var (
	ErrProjectNotFound         = errors.New("project not found")
	ErrSessionActionInProgress = errors.New("project action already in progress")
	ErrProjectStyleLocked      = errors.New("project style is locked")
)

const (
	webEventTypeHostEvent   = "host_event"
	webEventTypeStreamDelta = "stream_delta"
	webEventTypeStreamClear = "stream_clear"
	webEventTypeSnapshot    = "snapshot"
	webEventTypeCoCreate    = "cocreate_state"

	webEventHistoryLimit = 1000

	projectActionKindAdaptationAnalysis = "adaptation_analysis"
)

type SessionManager struct {
	cfg    bootstrap.Config
	bundle assets.Bundle
	store  *ProjectStore

	mu       sync.Mutex
	sessions map[string]*ProjectSession
}

type projectHost interface {
	Snapshot() host.UISnapshot
	PrepareUserRules(string) error
	PrepareExternalSourceUserRules(string) error
	SetWordBudget(*domain.WordBudget) error
	StartPrepared(string) error
	Abort() bool
	Resume() (string, error)
	Continue(string) error
	Steer(string) error
	CoCreateStream(context.Context, []host.CoCreateMessage, func(kind, text string)) (host.CoCreateReply, error)
	StageCoCreateStream(context.Context, []host.CoCreateMessage, func(kind, text string)) (host.CoCreateReply, error)
	AdaptCoCreateStream(context.Context, []host.CoCreateMessage, func(kind, text string)) (host.CoCreateReply, error)
	PauseForCoCreate() bool
	ResumeFromCoCreate(string) error
	CancelCoCreate()
	ImportFrom(context.Context, imp.Options) (<-chan imp.Event, error)
	SimulateFromDir(context.Context, string) (<-chan sim.Event, error)
	ImportSimulationProfile(context.Context, string) (<-chan sim.Event, error)
	PrepareAdaptationSource(context.Context, string) (<-chan adapt.Event, error)
	BuildAdaptationProposal(adapt.ProposalOptions) (*domain.AdaptationPlan, error)
	ConfirmAdaptationProposal() (*domain.AdaptationPlan, error)
	StartAdaptationPreparedWithOptions(adapt.ProposalOptions) error
	Export(context.Context, exp.Options) (*exp.Result, error)
	ReplayQueue(int64) ([]domain.RuntimeQueueItem, error)
	ConfiguredProviders() []string
	ConfiguredModels(string) []string
	CurrentModelSelection(string) (string, string, bool)
	SwitchModel(string, string, string) error
	AddProviderModel(string, string, bootstrap.ProviderConfig, string) error
	StartGrokLogin(string, string) (grokauth.LoginStart, error)
	PollGrokLogin() (grokauth.LoginPoll, error)
	CompleteGrokLogin(string) (grokauth.AuthStatus, error)
	GrokLoginStatus(string) grokauth.AuthStatus
	CurrentThinking(string) string
	SetRoleThinking(string, string) error
	Events() <-chan host.Event
	Stream() <-chan string
	Done() <-chan struct{}
	Close()
}

func NewSessionManager(cfg bootstrap.Config, bundle assets.Bundle, store *ProjectStore) *SessionManager {
	return &SessionManager{
		cfg:      cfg,
		bundle:   bundle,
		store:    store,
		sessions: make(map[string]*ProjectSession),
	}
}

func (m *SessionManager) SetConfig(cfg bootstrap.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cloneWebConfig(cfg)
}

func (m *SessionManager) Open(id string) (*ProjectSession, ProjectManifest, error) {
	id = strings.TrimSpace(id)
	if err := validateProjectID(id); err != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	manifest, err := m.store.OpenProject(id)
	if err != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	if session, ok := m.sessions[id]; ok {
		session.SetManifest(manifest)
		return session, manifest, nil
	}

	h, err := m.store.OpenProjectHost(cloneWebConfig(m.cfg), m.bundle, manifest)
	if err != nil {
		return nil, ProjectManifest{}, err
	}
	session, err := NewProjectSession(manifest, h)
	if err != nil {
		h.Close()
		return nil, ProjectManifest{}, err
	}
	m.sessions[id] = session
	return session, manifest, nil
}

func (m *SessionManager) SetProjectStyle(id, style string) (*ProjectSession, ProjectManifest, error) {
	id = strings.TrimSpace(id)
	if err := validateProjectID(id); err != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	style = assets.NormalizeStyleID(style)

	m.mu.Lock()
	defer m.mu.Unlock()

	manifest, err := m.store.OpenProject(id)
	if err != nil {
		return nil, ProjectManifest{}, fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	session, ok := m.sessions[id]
	if !ok {
		h, err := m.store.OpenProjectHost(m.cfg, m.bundle, manifest)
		if err != nil {
			return nil, ProjectManifest{}, err
		}
		session, err = NewProjectSession(manifest, h)
		if err != nil {
			h.Close()
			return nil, ProjectManifest{}, err
		}
		m.sessions[id] = session
	}

	unlock, err := session.beginAction()
	if err != nil {
		return nil, ProjectManifest{}, err
	}
	defer unlock()

	snapshot := session.Snapshot()
	if snapshot.IsRunning || snapshotHasExistingBook(snapshot) {
		return nil, ProjectManifest{}, fmt.Errorf("%w: cannot change style after writing has started", ErrProjectStyleLocked)
	}

	session.Close()
	delete(m.sessions, id)
	if err := m.store.SaveProjectStyle(manifest, style); err != nil {
		return nil, ProjectManifest{}, err
	}
	h, err := m.store.OpenProjectHost(m.cfg, m.bundle, manifest)
	if err != nil {
		return nil, ProjectManifest{}, err
	}
	next, err := NewProjectSession(manifest, h)
	if err != nil {
		h.Close()
		return nil, ProjectManifest{}, err
	}
	m.sessions[id] = next
	return next, manifest, nil
}

func (m *SessionManager) ActiveProjectIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (m *SessionManager) Project(id string) *ProjectSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[strings.TrimSpace(id)]
}

func (m *SessionManager) CloseProject(id string) bool {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	session, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if ok {
		session.Close()
	}
	return ok
}

func (m *SessionManager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*ProjectSession, 0, len(m.sessions))
	for id, session := range m.sessions {
		sessions = append(sessions, session)
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	for _, session := range sessions {
		session.Close()
	}
}

type ProjectSession struct {
	manifest ProjectManifest
	host     projectHost

	mu             sync.Mutex
	actionMu       sync.Mutex
	actionCancelMu sync.Mutex
	actionCancel   context.CancelFunc
	actionKind     string
	nextSeq        int64
	history        []WebEvent
	hostEventAt    map[string]int
	subscribers    map[chan WebEvent]struct{}
	cocreate       *webCoCreateSession
	closed         bool
}

func NewProjectSession(manifest ProjectManifest, h projectHost) (*ProjectSession, error) {
	session := &ProjectSession{
		manifest:    manifest,
		host:        h,
		hostEventAt: make(map[string]int),
		subscribers: make(map[chan WebEvent]struct{}),
	}
	if err := session.seedHistory(); err != nil {
		return nil, err
	}
	go session.pump()
	return session, nil
}

func (s *ProjectSession) SetManifest(manifest ProjectManifest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifest = manifest
}

func (s *ProjectSession) Snapshot() host.UISnapshot {
	return s.host.Snapshot()
}

func (s *ProjectSession) ModelConfig() apiModelConfig {
	providers := s.host.ConfiguredProviders()
	outProviders := make([]apiModelProvider, 0, len(providers))
	for _, provider := range providers {
		outProviders = append(outProviders, apiModelProvider{
			Name:   provider,
			Models: s.host.ConfiguredModels(provider),
		})
	}
	roles := make([]apiModelRoute, 0, len(modelConfigRoles))
	for _, role := range modelConfigRoles {
		provider, model, explicit := s.host.CurrentModelSelection(role)
		roles = append(roles, apiModelRoute{
			Role:            normalizeModelRole(role),
			Provider:        provider,
			Model:           model,
			Explicit:        explicit,
			ReasoningEffort: s.host.CurrentThinking(role),
		})
	}
	return apiModelConfig{
		Providers: outProviders,
		Roles:     roles,
		ThinkingLevels: []string{
			"",
			"off",
			"low",
			"medium",
			"high",
			"xhigh",
			"max",
		},
		ThinkingRule: "default applies to coordinator, architect, writer, and editor unless that role has its own reasoning_effort",
	}
}

func (s *ProjectSession) SwitchModel(role, provider, model string) (apiModelConfig, error) {
	if err := s.host.SwitchModel(normalizeModelRole(role), provider, model); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) SetRoleThinking(role, level string) (apiModelConfig, error) {
	if err := s.host.SetRoleThinking(normalizeModelRole(role), level); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) AddOpenAICompatibleModel(role, provider, model, baseURL, apiKey, api string) (apiModelConfig, error) {
	pc := bootstrap.ProviderConfig{
		Type:    "openai",
		API:     strings.TrimSpace(api),
		APIKey:  strings.TrimSpace(apiKey),
		BaseURL: strings.TrimSpace(baseURL),
	}
	if pc.API == "" {
		pc.API = "chat"
	}
	return s.AddProviderModel(role, provider, model, pc)
}

func (s *ProjectSession) AddProviderModel(role, provider, model string, pc bootstrap.ProviderConfig) (apiModelConfig, error) {
	if err := s.host.AddProviderModel(normalizeModelRole(role), provider, pc, model); err != nil {
		return apiModelConfig{}, err
	}
	s.AppendSnapshot()
	return s.ModelConfig(), nil
}

func (s *ProjectSession) StartGrokLogin(accountID, accountName string) (grokauth.LoginStart, error) {
	return s.host.StartGrokLogin(accountID, accountName)
}

func (s *ProjectSession) PollGrokLogin() (grokauth.LoginPoll, error) {
	return s.host.PollGrokLogin()
}

func (s *ProjectSession) CompleteGrokLogin(callbackInput string) (grokauth.AuthStatus, error) {
	return s.host.CompleteGrokLogin(callbackInput)
}

func (s *ProjectSession) GrokLoginStatus(accountID string) grokauth.AuthStatus {
	return s.host.GrokLoginStatus(accountID)
}

func (s *ProjectSession) StartQuick(text string, targetTotalWords int) error {
	unlock, err := s.beginAction()
	if err != nil {
		return err
	}
	defer unlock()

	plan, err := startup.PrepareQuick(startup.Request{
		Mode:             startup.ModeQuick,
		UserPrompt:       text,
		OutputDir:        s.manifest.OutputDir,
		Interactive:      false,
		TargetTotalWords: targetTotalWords,
	})
	if err != nil {
		return err
	}
	if err := s.host.PrepareUserRules(plan.RawPrompt); err != nil {
		return err
	}
	if err := s.persistWordBudget(plan.WordBudget); err != nil {
		return err
	}
	if err := s.host.StartPrepared(plan.StartPrompt); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) Pause() bool {
	canceledKind, canceledAction := s.cancelCurrentAction()
	stopped := s.host.Abort()
	if canceledAction {
		s.appendActionCanceledEvent(canceledKind)
	}
	if stopped {
		s.AppendSnapshot()
	}
	if canceledAction && !stopped {
		s.AppendSnapshot()
	}
	return stopped || canceledAction
}

func (s *ProjectSession) Resume() (string, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return "", err
	}
	defer unlock()

	label, err := s.host.Resume()
	if err == nil {
		s.AppendSnapshot()
	}
	return label, err
}

func (s *ProjectSession) ImportExternalNovel(ctx context.Context, sourcePath string, resumeFrom int) ([]apiImportEvent, string, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return nil, "", err
	}
	defer unlock()

	events, err := s.host.ImportFrom(ctx, imp.Options{
		SourcePath: sourcePath,
		ResumeFrom: resumeFrom,
	})
	if err != nil {
		return nil, "", err
	}
	if events == nil {
		return nil, "", fmt.Errorf("import event stream is nil")
	}
	apiEvents, err := s.consumeImportEvents(ctx, events)
	if err != nil {
		s.AppendSnapshot()
		return apiEvents, "", err
	}
	label, err := s.host.Resume()
	s.AppendSnapshot()
	return apiEvents, label, err
}

func (s *ProjectSession) Continue(text string) error {
	unlock, err := s.beginAction()
	if err != nil {
		return err
	}
	defer unlock()

	if err := s.host.Continue(text); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) Steer(text string) error {
	unlock, err := s.beginAction()
	if err != nil {
		return err
	}
	defer unlock()

	if err := s.host.Steer(text); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) SimulateFromDir(ctx context.Context, dir string) ([]apiSimulationEvent, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return nil, err
	}
	defer unlock()

	events, err := s.host.SimulateFromDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	if events == nil {
		return nil, fmt.Errorf("simulation event stream is nil")
	}
	apiEvents, err := s.consumeSimulationEvents(ctx, events)
	s.AppendSnapshot()
	return apiEvents, err
}

func (s *ProjectSession) ImportSimulationProfile(ctx context.Context, path string) ([]apiSimulationEvent, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return nil, err
	}
	defer unlock()

	events, err := s.host.ImportSimulationProfile(ctx, path)
	if err != nil {
		return nil, err
	}
	if events == nil {
		return nil, fmt.Errorf("simulation import event stream is nil")
	}
	apiEvents, err := s.consumeSimulationEvents(ctx, events)
	s.AppendSnapshot()
	return apiEvents, err
}

func (s *ProjectSession) PrepareAdaptationSource(ctx context.Context, sourcePath string) ([]apiAdaptationEvent, error) {
	ctx, unlock, err := s.beginCancellableAction(ctx, projectActionKindAdaptationAnalysis)
	if err != nil {
		return nil, err
	}
	defer unlock()

	events, err := s.host.PrepareAdaptationSource(ctx, sourcePath)
	if err != nil {
		return nil, err
	}
	if events == nil {
		return nil, fmt.Errorf("adaptation event stream is nil")
	}
	apiEvents, err := s.consumeAdaptationEvents(ctx, events)
	s.AppendSnapshot()
	return apiEvents, err
}

func (s *ProjectSession) StartAdaptationPrepared(options adapt.ProposalOptions) error {
	unlock, err := s.beginAction()
	if err != nil {
		return err
	}
	defer unlock()

	if err := s.host.PrepareExternalSourceUserRules(options.Brief); err != nil {
		return err
	}
	if err := s.host.StartAdaptationPreparedWithOptions(options); err != nil {
		return err
	}
	s.AppendSnapshot()
	return nil
}

func (s *ProjectSession) BuildAdaptationProposal(options adapt.ProposalOptions) (*domain.AdaptationPlan, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return nil, err
	}
	defer unlock()

	eventID, startedAt := s.appendAdaptationProposalStarted(options)
	if err := s.host.PrepareExternalSourceUserRules(options.Brief); err != nil {
		s.appendAdaptationProposalFinished(eventID, startedAt, options, err)
		return nil, err
	}
	proposal, err := s.host.BuildAdaptationProposal(options)
	if err != nil {
		s.appendAdaptationProposalFinished(eventID, startedAt, options, err)
		return nil, err
	}
	s.appendAdaptationProposalFinished(eventID, startedAt, options, nil)
	s.AppendSnapshot()
	return proposal, nil
}

func (s *ProjectSession) ConfirmAdaptationProposal() (*domain.AdaptationPlan, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return nil, err
	}
	defer unlock()

	plan, err := s.host.ConfirmAdaptationProposal()
	if err != nil {
		return nil, err
	}
	s.AppendSnapshot()
	return plan, nil
}

func (s *ProjectSession) Export(ctx context.Context, opts exp.Options) (*exp.Result, error) {
	result, err := s.host.Export(ctx, opts)
	if err == nil {
		s.AppendSnapshot()
	}
	return result, err
}

func (s *ProjectSession) BeginCoCreate(ctx context.Context, req webCoCreateBeginRequest) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate != nil {
		return s.cocreate.apiState(), fmt.Errorf("co-create already started")
	}
	state, err := newWebCoCreateSession(req)
	if err != nil {
		return webCoCreateState{}, err
	}
	if state.kind == webCoCreateKindStage {
		if !s.host.PauseForCoCreate() {
			return webCoCreateState{}, fmt.Errorf("cannot enter stage co-create")
		}
		s.AppendSnapshot()
	}
	s.cocreate = state
	return s.runCoCreateLocked(ctx)
}

func (s *ProjectSession) SendCoCreate(ctx context.Context, text, source string) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	if err := s.cocreate.appendUser(text, source); err != nil {
		return webCoCreateState{}, err
	}
	return s.runCoCreateLocked(ctx)
}

func (s *ProjectSession) ReviseCoCreate(ctx context.Context, req webCoCreateReviseRequest) (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	if err := s.cocreate.reviseUser(req.MessageID, req.Text); err != nil {
		return webCoCreateState{}, err
	}
	return s.runCoCreateLocked(ctx)
}

func (s *ProjectSession) CommitCoCreate() (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	if err := s.cocreate.requireReadyDraft(); err != nil {
		return webCoCreateState{}, err
	}
	state := s.cocreate
	switch state.kind {
	case webCoCreateKindStage:
		if err := s.host.ResumeFromCoCreate(state.draftPrompt()); err != nil {
			return state.apiState(), err
		}
	case webCoCreateKindAdapt:
		if err := s.host.PrepareExternalSourceUserRules(state.draftPrompt()); err != nil {
			return state.apiState(), err
		}
		proposal, err := s.host.BuildAdaptationProposal(adapt.ProposalOptions{
			Brief:         state.draftPrompt(),
			SourcePath:    state.sourcePath,
			Granularity:   state.adaptGranularity,
			RewritePolicy: state.adaptRewritePolicy,
			WordTolerance: state.adaptWordTolerance,
		})
		if err != nil {
			return state.apiState(), err
		}
		state.adaptationProposal = proposal
	default:
		plan, err := state.session.BuildPlanWithWordBudget(state.targetTotalWords)
		if err != nil {
			return state.apiState(), err
		}
		if err := s.host.PrepareUserRules(plan.RawPrompt); err != nil {
			return state.apiState(), err
		}
		if err := s.persistWordBudget(plan.WordBudget); err != nil {
			return state.apiState(), err
		}
		if err := s.host.StartPrepared(plan.StartPrompt); err != nil {
			return state.apiState(), err
		}
	}
	api := state.apiState()
	s.cocreate = nil
	s.AppendSnapshot()
	return api, nil
}

func (s *ProjectSession) persistWordBudget(budget *domain.WordBudget) error {
	if budget == nil || budget.TargetTotalWords <= 0 {
		return nil
	}
	if err := s.host.SetWordBudget(budget); err != nil {
		return fmt.Errorf("save word budget: %w", err)
	}
	return nil
}

func (s *ProjectSession) CancelCoCreate() (webCoCreateState, error) {
	unlock, err := s.beginAction()
	if err != nil {
		return webCoCreateState{}, err
	}
	defer unlock()

	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	state := s.cocreate.apiState()
	if s.cocreate.kind == webCoCreateKindStage {
		s.host.CancelCoCreate()
		s.AppendSnapshot()
	}
	s.cocreate = nil
	return state, nil
}

func (s *ProjectSession) runCoCreateLocked(ctx context.Context) (webCoCreateState, error) {
	if s.cocreate == nil {
		return webCoCreateState{}, fmt.Errorf("co-create has not started")
	}
	var stream func(context.Context, []host.CoCreateMessage, func(kind, text string)) (host.CoCreateReply, error)
	switch s.cocreate.kind {
	case webCoCreateKindStage:
		stream = s.host.StageCoCreateStream
	case webCoCreateKindAdapt:
		stream = s.host.AdaptCoCreateStream
	default:
		stream = s.host.CoCreateStream
	}
	onProgress := func(kind, text string) {
		if s.cocreate == nil {
			return
		}
		s.cocreate.session.ApplyDelta(kind, text)
		s.appendCoCreateState(s.cocreate.apiState())
	}
	reply, err := stream(ctx, s.cocreate.session.History(), onProgress)
	if err != nil {
		return s.cocreate.apiState(), err
	}
	s.cocreate.applyReply(reply)
	s.appendCoCreateState(s.cocreate.apiState())
	return s.cocreate.apiState(), nil
}

func (s *ProjectSession) beginAction() (func(), error) {
	if !s.actionMu.TryLock() {
		return nil, ErrSessionActionInProgress
	}
	return s.actionMu.Unlock, nil
}

func (s *ProjectSession) beginCancellableAction(parent context.Context, kind string) (context.Context, func(), error) {
	unlock, err := s.beginAction()
	if err != nil {
		return nil, nil, err
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.setActionCancel(kind, cancel)
	return ctx, func() {
		s.clearActionCancel()
		cancel()
		unlock()
	}, nil
}

func (s *ProjectSession) isActionRunning(kind string) bool {
	s.actionCancelMu.Lock()
	defer s.actionCancelMu.Unlock()
	return s.actionCancel != nil && s.actionKind == strings.TrimSpace(kind)
}

func (s *ProjectSession) setActionCancel(kind string, cancel context.CancelFunc) {
	s.actionCancelMu.Lock()
	defer s.actionCancelMu.Unlock()
	s.actionKind = strings.TrimSpace(kind)
	s.actionCancel = cancel
}

func (s *ProjectSession) clearActionCancel() {
	s.actionCancelMu.Lock()
	defer s.actionCancelMu.Unlock()
	s.actionKind = ""
	s.actionCancel = nil
}

func (s *ProjectSession) cancelCurrentAction() (string, bool) {
	s.actionCancelMu.Lock()
	defer s.actionCancelMu.Unlock()
	if s.actionCancel == nil {
		return "", false
	}
	kind := s.actionKind
	cancel := s.actionCancel
	s.actionKind = ""
	s.actionCancel = nil
	cancel()
	return kind, true
}

func (s *ProjectSession) AppendSnapshot() WebEvent {
	return s.append(WebEvent{
		Type:     webEventTypeSnapshot,
		Snapshot: s.host.Snapshot(),
	})
}

func (s *ProjectSession) ServeEvents(ctx context.Context, w http.ResponseWriter, after int64) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("response writer does not support streaming")
	}

	w.Header().Set("content-type", "text/event-stream; charset=utf-8")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("connection", "keep-alive")
	w.Header().Set("x-accel-buffering", "no")

	s.AppendSnapshot()
	history, ch, unsubscribe := s.Subscribe(after)
	defer unsubscribe()

	for _, ev := range history {
		if err := writeSSEEvent(w, ev); err != nil {
			return err
		}
	}
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := writeSSEEvent(w, ev); err != nil {
				return err
			}
			flusher.Flush()
		}
	}
}

func (s *ProjectSession) Subscribe(after int64) ([]WebEvent, <-chan WebEvent, func()) {
	ch := make(chan WebEvent, 256)
	s.mu.Lock()
	if s.closed {
		close(ch)
		s.mu.Unlock()
		return nil, ch, func() {}
	}
	s.subscribers[ch] = struct{}{}
	history := s.historyAfterLocked(after)
	s.mu.Unlock()

	unsubscribe := func() {
		s.mu.Lock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
	return history, ch, unsubscribe
}

func (s *ProjectSession) HistoryAfter(after int64) []WebEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.historyAfterLocked(after)
}

func (s *ProjectSession) Close() {
	if s.host != nil {
		s.host.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, ch)
	}
}

func (s *ProjectSession) seedHistory() error {
	items, err := s.host.ReplayQueue(0)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range items {
		if item.Seq > s.nextSeq {
			s.nextSeq = item.Seq
		}
		ev, ok := webEventFromQueueItem(s.manifest.ID, item)
		if !ok {
			continue
		}
		s.upsertLocked(ev)
	}
	return nil
}

func (s *ProjectSession) pump() {
	events := s.host.Events()
	stream := s.host.Stream()
	done := s.host.Done()
	for events != nil || stream != nil || done != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			s.appendHostEvent(ev)
			s.AppendSnapshot()
		case delta, ok := <-stream:
			if !ok {
				stream = nil
				continue
			}
			if delta == host.StreamClearSentinel {
				s.appendStreamClear()
			} else {
				s.appendStreamDelta(delta)
			}
		case _, ok := <-done:
			if !ok {
				done = nil
				continue
			}
			s.AppendSnapshot()
		}
	}
}

func (s *ProjectSession) appendHostEvent(ev host.Event) WebEvent {
	return s.append(WebEvent{
		Type:        webEventTypeHostEvent,
		HostEventID: strings.TrimSpace(ev.ID),
		Event:       apiHostEventFromHost(ev),
	})
}

func (s *ProjectSession) consumeSimulationEvents(ctx context.Context, events <-chan sim.Event) ([]apiSimulationEvent, error) {
	var out []apiSimulationEvent
	var runErr error
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return out, runErr
			}
			apiEvent := apiSimulationEventFromSim(ev)
			out = append(out, apiEvent)
			s.appendSimulationEvent(apiEvent)
			if ev.Err != nil {
				message := strings.TrimSpace(ev.Message)
				if message == "" {
					message = ev.Err.Error()
				} else {
					message = fmt.Sprintf("%s: %v", message, ev.Err)
				}
				runErr = simulationRunError{message: message}
			}
		}
	}
}

func (s *ProjectSession) consumeImportEvents(ctx context.Context, events <-chan imp.Event) ([]apiImportEvent, error) {
	var out []apiImportEvent
	var runErr error
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return out, runErr
			}
			apiEvent := apiImportEventFromImp(ev)
			out = append(out, apiEvent)
			s.appendImportEvent(apiEvent)
			if ev.Err != nil {
				message := strings.TrimSpace(ev.Message)
				if message == "" {
					message = ev.Err.Error()
				} else {
					message = fmt.Sprintf("%s: %v", message, ev.Err)
				}
				runErr = importRunError{message: message}
			}
		}
	}
}

func (s *ProjectSession) consumeAdaptationEvents(ctx context.Context, events <-chan adapt.Event) ([]apiAdaptationEvent, error) {
	var out []apiAdaptationEvent
	var runErr error
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				ev := apiAdaptationEvent{
					Time:    time.Now().UTC(),
					Stage:   string(adapt.StagePaused),
					Message: "原文分析已暂停，可再次点击分析继续",
				}
				out = append(out, ev)
				s.appendAdaptationEvent(ev)
				return out, adaptationPausedError{message: ev.Message}
			}
			return out, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return out, runErr
			}
			apiEvent := apiAdaptationEventFromAdapt(ev)
			out = append(out, apiEvent)
			s.appendAdaptationEvent(apiEvent)
			if ev.Err != nil {
				message := strings.TrimSpace(ev.Message)
				if message == "" {
					message = ev.Err.Error()
				} else {
					message = fmt.Sprintf("%s: %v", message, ev.Err)
				}
				runErr = adaptationRunError{message: message}
			}
		}
	}
}

func (s *ProjectSession) appendSimulationEvent(ev apiSimulationEvent) WebEvent {
	level := "info"
	if ev.Error != "" {
		level = "error"
	} else if ev.Stage == string(sim.StageDone) {
		level = "success"
	}
	return s.appendHostEvent(host.Event{
		Time:     ev.Time,
		Category: "SIMULATE",
		Agent:    "web",
		Summary:  ev.Message,
		Detail:   ev.Error,
		Kind:     ev.Stage,
		Level:    level,
	})
}

func (s *ProjectSession) appendImportEvent(ev apiImportEvent) WebEvent {
	level := "info"
	if ev.Error != "" {
		level = "error"
	} else if ev.Stage == string(imp.StageDone) {
		level = "success"
	}
	return s.appendHostEvent(host.Event{
		Time:     ev.Time,
		Category: "IMPORT",
		Agent:    "web",
		Summary:  ev.Message,
		Detail:   ev.Error,
		Kind:     ev.Stage,
		Level:    level,
	})
}

func (s *ProjectSession) appendAdaptationEvent(ev apiAdaptationEvent) WebEvent {
	level := "info"
	if ev.Error != "" {
		level = "error"
	} else if ev.Stage == string(adapt.StagePaused) {
		level = "warn"
	} else if ev.Stage == string(adapt.StageDone) {
		level = "success"
	}
	return s.appendHostEvent(host.Event{
		Time:     ev.Time,
		Category: "ADAPT",
		Agent:    "web",
		Summary:  ev.Message,
		Detail:   ev.Error,
		Kind:     ev.Stage,
		Level:    level,
	})
}

func (s *ProjectSession) appendAdaptationProposalStarted(options adapt.ProposalOptions) (string, time.Time) {
	startedAt := time.Now().UTC()
	eventID := fmt.Sprintf("adapt-proposal-%d", startedAt.UnixNano())
	s.appendHostEvent(host.Event{
		ID:       eventID,
		Time:     startedAt,
		Category: "ADAPT",
		Agent:    "web",
		Summary:  adaptationProposalEventSummary("正在生成改编提案", options),
		Kind:     "proposal",
		Level:    "info",
	})
	return eventID, startedAt
}

func (s *ProjectSession) appendAdaptationProposalFinished(eventID string, startedAt time.Time, options adapt.ProposalOptions, err error) {
	if eventID == "" {
		return
	}
	finishedAt := time.Now().UTC()
	summary := adaptationProposalEventSummary("改编提案已生成", options)
	level := "success"
	var detail string
	var failed bool
	if err != nil {
		summary = adaptationProposalEventSummary("改编提案生成失败", options)
		level = "error"
		detail = err.Error()
		failed = true
	}
	s.appendHostEvent(host.Event{
		ID:         eventID,
		Time:       startedAt,
		FinishedAt: finishedAt,
		Failed:     failed,
		Category:   "ADAPT",
		Agent:      "web",
		Summary:    summary,
		Detail:     detail,
		Kind:       "proposal",
		Level:      level,
		Duration:   finishedAt.Sub(startedAt),
	})
}

func adaptationProposalEventSummary(action string, options adapt.ProposalOptions) string {
	mode := strings.TrimSpace(options.Granularity)
	if mode == "" {
		return action
	}
	return fmt.Sprintf("%s（%s）", action, mode)
}

func (s *ProjectSession) appendActionCanceledEvent(kind string) WebEvent {
	summary := "当前操作已请求暂停"
	if kind == projectActionKindAdaptationAnalysis {
		summary = "原文分析已请求暂停"
	}
	return s.appendHostEvent(host.Event{
		Time:     time.Now().UTC(),
		Category: "SYSTEM",
		Agent:    "web",
		Summary:  summary,
		Kind:     "paused",
		Level:    "warn",
	})
}

func (s *ProjectSession) appendStreamDelta(delta string) WebEvent {
	return s.append(WebEvent{
		Type: webEventTypeStreamDelta,
		Stream: &APIStreamEvent{
			Text: delta,
		},
	})
}

func (s *ProjectSession) appendStreamClear() WebEvent {
	return s.append(WebEvent{
		Type:   webEventTypeStreamClear,
		Stream: &APIStreamEvent{Clear: true},
	})
}

func (s *ProjectSession) appendCoCreateState(state webCoCreateState) WebEvent {
	return s.append(WebEvent{
		Type:     webEventTypeCoCreate,
		CoCreate: &state,
	})
}

func (s *ProjectSession) append(ev WebEvent) WebEvent {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.ProjectID == "" {
		ev.ProjectID = s.manifest.ID
	}
	if s.closed {
		return ev
	}
	s.nextSeq++
	ev.Seq = s.nextSeq
	s.upsertLocked(ev)
	for ch := range s.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
	return ev
}

func (s *ProjectSession) upsertLocked(ev WebEvent) {
	if ev.Type == webEventTypeHostEvent && ev.HostEventID != "" {
		if idx, ok := s.hostEventAt[ev.HostEventID]; ok {
			s.history = append(s.history[:idx], s.history[idx+1:]...)
			s.rebuildHostEventIndexLocked()
		}
	}
	s.history = append(s.history, ev)
	if ev.Type == webEventTypeHostEvent && ev.HostEventID != "" {
		s.hostEventAt[ev.HostEventID] = len(s.history) - 1
	}
	s.trimHistoryLocked()
}

func (s *ProjectSession) trimHistoryLocked() {
	if len(s.history) <= webEventHistoryLimit {
		return
	}
	s.history = append([]WebEvent(nil), s.history[len(s.history)-webEventHistoryLimit:]...)
	s.rebuildHostEventIndexLocked()
}

func (s *ProjectSession) rebuildHostEventIndexLocked() {
	s.hostEventAt = make(map[string]int)
	for i, ev := range s.history {
		if ev.Type == webEventTypeHostEvent && ev.HostEventID != "" {
			s.hostEventAt[ev.HostEventID] = i
		}
	}
}

func (s *ProjectSession) historyAfterLocked(after int64) []WebEvent {
	out := make([]WebEvent, 0, len(s.history))
	for _, ev := range s.history {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	return out
}

type WebEvent struct {
	Seq         int64             `json:"seq"`
	Type        string            `json:"type"`
	ProjectID   string            `json:"project_id"`
	Time        time.Time         `json:"time"`
	HostEventID string            `json:"host_event_id,omitempty"`
	Event       *APIHostEvent     `json:"event,omitempty"`
	Stream      *APIStreamEvent   `json:"stream,omitempty"`
	Snapshot    any               `json:"snapshot,omitempty"`
	CoCreate    *webCoCreateState `json:"cocreate,omitempty"`
}

type APIHostEvent struct {
	ID             string     `json:"id,omitempty"`
	Time           time.Time  `json:"time"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	Failed         bool       `json:"failed,omitempty"`
	Category       string     `json:"category,omitempty"`
	Agent          string     `json:"agent,omitempty"`
	Summary        string     `json:"summary,omitempty"`
	Detail         string     `json:"detail,omitempty"`
	Kind           string     `json:"kind,omitempty"`
	Level          string     `json:"level,omitempty"`
	Depth          int        `json:"depth,omitempty"`
	DurationMillis int64      `json:"duration_ms,omitempty"`
	Running        bool       `json:"running"`
}

type APIStreamEvent struct {
	Text  string `json:"text,omitempty"`
	Clear bool   `json:"clear,omitempty"`
}

func apiHostEventFromHost(ev host.Event) *APIHostEvent {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	api := &APIHostEvent{
		ID:             ev.ID,
		Time:           ev.Time,
		Failed:         ev.Failed,
		Category:       ev.Category,
		Agent:          ev.Agent,
		Summary:        ev.Summary,
		Detail:         ev.Detail,
		Kind:           ev.Kind,
		Level:          ev.Level,
		Depth:          ev.Depth,
		DurationMillis: ev.Duration.Milliseconds(),
		Running:        ev.Running(),
	}
	if !ev.FinishedAt.IsZero() {
		finished := ev.FinishedAt
		api.FinishedAt = &finished
	}
	return api
}

func webEventFromQueueItem(projectID string, item domain.RuntimeQueueItem) (WebEvent, bool) {
	base := WebEvent{
		Seq:       item.Seq,
		ProjectID: projectID,
		Time:      item.Time,
	}
	switch item.Kind {
	case domain.RuntimeQueueUIEvent:
		api := apiHostEventFromQueueItem(item)
		base.Type = webEventTypeHostEvent
		base.HostEventID = api.ID
		base.Event = api
		return base, true
	case domain.RuntimeQueueStreamDelta:
		text := host.ReplayDeltaText(item)
		if text == "" {
			return WebEvent{}, false
		}
		base.Type = webEventTypeStreamDelta
		base.Stream = &APIStreamEvent{Text: text}
		return base, true
	case domain.RuntimeQueueStreamClear:
		base.Type = webEventTypeStreamClear
		base.Stream = &APIStreamEvent{Clear: true}
		return base, true
	default:
		return WebEvent{}, false
	}
}

func apiHostEventFromQueueItem(item domain.RuntimeQueueItem) *APIHostEvent {
	ev := host.Event{
		Time:     item.Time,
		Category: item.Category,
		Summary:  item.Summary,
	}
	if item.Payload != nil {
		if data, err := json.Marshal(item.Payload); err == nil {
			_ = json.Unmarshal(data, &ev)
		}
	}
	if ev.Time.IsZero() {
		ev.Time = item.Time
	}
	if ev.Category == "" {
		ev.Category = item.Category
	}
	if ev.Summary == "" {
		ev.Summary = item.Summary
	}
	return apiHostEventFromHost(ev)
}

func writeSSEEvent(w http.ResponseWriter, ev WebEvent) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\n", ev.Seq); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", ev.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return nil
}
