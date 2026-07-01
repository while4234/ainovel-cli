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
	"github.com/voocel/ainovel-cli/internal/host"
)

var (
	ErrProjectNotFound         = errors.New("project not found")
	ErrSessionActionInProgress = errors.New("project action already in progress")
)

const (
	webEventTypeHostEvent   = "host_event"
	webEventTypeStreamDelta = "stream_delta"
	webEventTypeStreamClear = "stream_clear"
	webEventTypeSnapshot    = "snapshot"

	webEventHistoryLimit = 1000
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
	Resume() (string, error)
	Continue(string) error
	Steer(string) error
	ReplayQueue(int64) ([]domain.RuntimeQueueItem, error)
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

	h, err := m.store.OpenProjectHost(m.cfg, m.bundle, manifest)
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

	mu          sync.Mutex
	actionMu    sync.Mutex
	nextSeq     int64
	history     []WebEvent
	hostEventAt map[string]int
	subscribers map[chan WebEvent]struct{}
	closed      bool
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

func (s *ProjectSession) beginAction() (func(), error) {
	if !s.actionMu.TryLock() {
		return nil, ErrSessionActionInProgress
	}
	return s.actionMu.Unlock, nil
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
	Seq         int64           `json:"seq"`
	Type        string          `json:"type"`
	ProjectID   string          `json:"project_id"`
	Time        time.Time       `json:"time"`
	HostEventID string          `json:"host_event_id,omitempty"`
	Event       *APIHostEvent   `json:"event,omitempty"`
	Stream      *APIStreamEvent `json:"stream,omitempty"`
	Snapshot    any             `json:"snapshot,omitempty"`
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
