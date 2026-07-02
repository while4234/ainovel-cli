package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

const (
	webCoCreateKindNormal = "normal"
	webCoCreateKindStage  = "stage"
	webCoCreateKindAdapt  = "adapt"

	stageCoCreateOpener     = "我先暂停一下，想和你一起规划接下来的走向。"
	stageCoCreateSystemLine = "已暂停创作，进入阶段共创。AI 会结合当前故事进度，和你一起规划接下来的走向。"
	adaptCoCreateSystemLine = "原书分析和模式选择完成，进入改编共创。AI 会锁定已选模式，帮你确认具体改编目标。"
)

type webCoCreateBeginRequest struct {
	Kind             string  `json:"kind"`
	Initial          string  `json:"initial"`
	SourceFile       string  `json:"source_file"`
	Mode             string  `json:"mode"`
	Tolerance        float64 `json:"word_tolerance"`
	TargetTotalWords int     `json:"target_total_words"`

	sourcePath string
}

type webCoCreateSendRequest struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

type webCoCreateReviseRequest struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

type webCoCreateMessage struct {
	ID           string `json:"id"`
	Role         string `json:"role"`
	Content      string `json:"content"`
	Editable     bool   `json:"editable,omitempty"`
	Source       string `json:"source,omitempty"`
	historyIndex int
}

type webCoCreateState struct {
	Kind             string               `json:"kind"`
	Active           bool                 `json:"active"`
	Messages         []webCoCreateMessage `json:"messages"`
	DraftPrompt      string               `json:"draft_prompt"`
	Ready            bool                 `json:"ready"`
	Suggestions      []string             `json:"suggestions"`
	StreamThinking   string               `json:"stream_thinking,omitempty"`
	StreamReply      string               `json:"stream_reply,omitempty"`
	AdaptMode        string               `json:"adapt_mode,omitempty"`
	RewritePolicy    string               `json:"rewrite_policy,omitempty"`
	WordTolerance    float64              `json:"word_tolerance,omitempty"`
	TargetTotalWords int                  `json:"target_total_words,omitempty"`
	SourceFile       string               `json:"source_file,omitempty"`
	CanStart         bool                 `json:"can_start"`
	ModeLocked       bool                 `json:"mode_locked,omitempty"`
	CommittedLabel   string               `json:"committed_label,omitempty"`
}

type webCoCreateSession struct {
	kind               string
	session            *startup.CoCreateSession
	messages           []webCoCreateMessage
	nextMessageSeq     int
	sourceFile         string
	sourcePath         string
	adaptGranularity   string
	adaptRewritePolicy string
	adaptWordTolerance float64
	targetTotalWords   int
}

func (s *Server) handleProjectCoCreateBegin(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req webCoCreateBeginRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid co-create request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	if strings.TrimSpace(req.Kind) == webCoCreateKindAdapt {
		if req.TargetTotalWords != 0 {
			writeError(w, http.StatusBadRequest, "target_total_words is only supported for normal co-create")
			return
		}
		mode := strings.TrimSpace(req.Mode)
		rewritePolicy, err := adaptationRewritePolicyForMode(mode)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		sourcePath, err := adaptationSourcePathFromName(req.SourceFile, manifest, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Mode = mode
		req.sourcePath = sourcePath
		req.SourceFile = strings.TrimSpace(req.SourceFile)
		if req.Tolerance <= 0 {
			req.Tolerance = adapt.DefaultWordTolerance
		}
		if rewritePolicy == "" {
			writeError(w, http.StatusBadRequest, "adaptation rewrite policy is required")
			return
		}
	}
	state, err := session.BeginCoCreate(r.Context(), req)
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCoCreateSend(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeCoCreateSendRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.SendCoCreate(r.Context(), req.Text, req.Source)
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCoCreateRevise(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req webCoCreateReviseRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid co-create revise request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.ReviseCoCreate(r.Context(), req)
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCoCreateCommit(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.CommitCoCreate()
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	state.CommittedLabel = coCreateCommitLabel(state.Kind)
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCoCreateCancel(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.CancelCoCreate()
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func newWebCoCreateSession(req webCoCreateBeginRequest) (*webCoCreateSession, error) {
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = webCoCreateKindNormal
	}
	switch kind {
	case webCoCreateKindNormal:
		initial := strings.TrimSpace(req.Initial)
		if initial == "" {
			return nil, fmt.Errorf("initial idea is required")
		}
		if req.TargetTotalWords < 0 {
			return nil, fmt.Errorf("target_total_words must be a non-negative integer")
		}
		targetTotalWords := req.TargetTotalWords
		state := &webCoCreateSession{
			kind:             kind,
			session:          startup.NewCoCreateSession(initial),
			targetTotalWords: targetTotalWords,
		}
		state.messages = append(state.messages, state.newMessage("user", initial, "custom", 0))
		return state, nil
	case webCoCreateKindStage:
		initial := strings.TrimSpace(req.Initial)
		if initial == "" {
			initial = stageCoCreateOpener
		}
		state := &webCoCreateSession{
			kind:    kind,
			session: startup.NewCoCreateSession(initial),
		}
		messages := []webCoCreateMessage{state.newMessage("system", stageCoCreateSystemLine, "", -1)}
		if initial != stageCoCreateOpener {
			messages = append(messages, state.newMessage("user", initial, "custom", 0))
		}
		state.messages = messages
		return state, nil
	case webCoCreateKindAdapt:
		granularity, ok := domain.StrictAdaptationGranularity(req.Mode)
		if !ok {
			return nil, fmt.Errorf("adaptation mode must be one of chapter, arc, free")
		}
		tolerance := req.Tolerance
		if tolerance <= 0 {
			tolerance = adapt.DefaultWordTolerance
		}
		rewritePolicy := domain.AdaptationRewritePolicyForGranularity(granularity)
		opener := adaptCoCreateOpener(granularity, rewritePolicy, tolerance)
		state := &webCoCreateSession{
			kind:               kind,
			session:            startup.NewCoCreateSession(opener),
			sourceFile:         strings.TrimSpace(req.SourceFile),
			sourcePath:         strings.TrimSpace(req.sourcePath),
			adaptGranularity:   granularity,
			adaptRewritePolicy: rewritePolicy,
			adaptWordTolerance: tolerance,
		}
		state.messages = []webCoCreateMessage{state.newMessage("system", adaptCoCreateSystemLine, "", -1)}
		return state, nil
	default:
		return nil, fmt.Errorf("co-create kind must be one of normal, stage, adapt")
	}
}

func (s *webCoCreateSession) newMessage(role, content, source string, historyIndex int) webCoCreateMessage {
	s.nextMessageSeq++
	message := webCoCreateMessage{
		ID:           fmt.Sprintf("m%d", s.nextMessageSeq),
		Role:         role,
		Content:      content,
		Source:       coCreateMessageSource(source),
		historyIndex: historyIndex,
	}
	message.Editable = role == "user" && historyIndex >= 0
	return message
}

func coCreateMessageSource(source string) string {
	switch strings.TrimSpace(source) {
	case "suggestion":
		return "suggestion"
	case "custom":
		return "custom"
	default:
		return ""
	}
}

func (s *webCoCreateSession) appendUser(text, source string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}
	historyIndex := len(s.session.History())
	s.session.AppendUser(text)
	if coCreateMessageSource(source) == "" {
		source = "custom"
	}
	s.messages = append(s.messages, s.newMessage("user", text, source, historyIndex))
	return nil
}

func (s *webCoCreateSession) applyReply(reply host.CoCreateReply) {
	historyIndex := len(s.session.History())
	s.session.ApplyReply(reply)
	if text := strings.TrimSpace(reply.Message); text != "" {
		s.messages = append(s.messages, s.newMessage("assistant", text, "", historyIndex))
	}
}

func (s *webCoCreateSession) reviseUser(messageID, text string) error {
	messageID = strings.TrimSpace(messageID)
	text = strings.TrimSpace(text)
	if messageID == "" {
		return fmt.Errorf("message_id is required")
	}
	if text == "" {
		return fmt.Errorf("text is required")
	}
	for idx := range s.messages {
		message := s.messages[idx]
		if message.ID != messageID {
			continue
		}
		if message.Role != "user" || !message.Editable || message.historyIndex < 0 {
			return fmt.Errorf("message is not editable")
		}
		history := s.session.History()
		if message.historyIndex >= len(history) || history[message.historyIndex].Role != "user" {
			return fmt.Errorf("message history is no longer editable")
		}
		history = append([]host.CoCreateMessage(nil), history[:message.historyIndex+1]...)
		history[message.historyIndex] = host.CoCreateMessage{Role: "user", Content: text}
		s.session.ResetHistory(history)
		s.messages = append([]webCoCreateMessage(nil), s.messages[:idx+1]...)
		s.messages[idx].Content = text
		if s.messages[idx].Source == "" {
			s.messages[idx].Source = "custom"
		}
		return nil
	}
	return fmt.Errorf("message not found")
}

func (s *webCoCreateSession) requireReadyDraft() error {
	if s == nil {
		return fmt.Errorf("co-create has not started")
	}
	if strings.TrimSpace(s.draftPrompt()) == "" {
		return fmt.Errorf("draft prompt is required")
	}
	return nil
}

func (s *webCoCreateSession) draftPrompt() string {
	if s == nil || s.session == nil {
		return ""
	}
	return strings.TrimSpace(s.session.DraftPrompt())
}

func (s *webCoCreateSession) apiState() webCoCreateState {
	if s == nil {
		return webCoCreateState{}
	}
	return webCoCreateState{
		Kind:             s.kind,
		Active:           true,
		Messages:         webCoCreateDisplayMessages(s.kind, s.messages),
		DraftPrompt:      s.draftPrompt(),
		Ready:            s.session.Ready(),
		Suggestions:      append([]string(nil), s.session.Suggestions()...),
		StreamThinking:   s.session.StreamThinking(),
		StreamReply:      normalizeWebCoCreateText(s.kind, s.session.StreamReply()),
		AdaptMode:        s.adaptGranularity,
		RewritePolicy:    s.adaptRewritePolicy,
		WordTolerance:    s.adaptWordTolerance,
		TargetTotalWords: s.targetTotalWords,
		SourceFile:       s.sourceFile,
		CanStart:         s.session.Ready() && strings.TrimSpace(s.session.DraftPrompt()) != "",
		ModeLocked:       s.kind == webCoCreateKindAdapt,
	}
}

func webCoCreateDisplayMessages(kind string, messages []webCoCreateMessage) []webCoCreateMessage {
	out := append([]webCoCreateMessage(nil), messages...)
	for i := range out {
		if out[i].Role == "assistant" {
			out[i].Content = normalizeWebCoCreateText(kind, out[i].Content)
		}
	}
	return out
}

func normalizeWebCoCreateText(kind string, text string) string {
	if text == "" {
		return ""
	}
	return strings.NewReplacer(
		"可以按 Ctrl+S 把方向交给创作引擎、继续创作", "可以点击「启动」把方向交给创作引擎并继续创作",
		"可以按 Ctrl+S 应用方向并继续创作", "可以点击「启动」应用方向并继续创作",
		"可以按 Ctrl+S 开始改编", "可以点击「启动」开始改编",
		"可以按 Ctrl+S 开始创作", "可以点击「启动」开始创作",
		"可以按 Ctrl+S 开始", "可以点击「启动」开始",
		"按 Ctrl+S 把方向交给创作引擎、继续创作", "点击「启动」把方向交给创作引擎并继续创作",
		"按 Ctrl+S 应用方向并继续创作", "点击「启动」应用方向并继续创作",
		"按 Ctrl+S 开始改编", "点击「启动」开始改编",
		"按 Ctrl+S 开始创作", "点击「启动」开始创作",
		"按 Ctrl+S 开始", "点击「启动」开始",
		"Ctrl+S", "点击「启动」",
	).Replace(text)
}

func adaptCoCreateOpener(granularity, rewritePolicy string, wordTolerance float64) string {
	return strings.TrimSpace(fmt.Sprintf(`我想基于这本小说做改编，已确认改编模式如下：

granularity=%s
rewrite_policy=%s
word_tolerance=%.2f
rewrite_policy_rule=chapter=>preserve_details;arc/free=>full_rewrite

请基于原书分析帮我确认具体改编目标。不要再询问或改动 chapter/arc/free 与 full_rewrite/preserve_details 这两个模式选择。`,
		granularity, rewritePolicy, wordTolerance))
}

func decodeCoCreateSendRequest(r *http.Request) (webCoCreateSendRequest, error) {
	var req webCoCreateSendRequest
	if err := decodeJSONBody(r, &req); err != nil {
		return req, fmt.Errorf("invalid request body: %w", err)
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return req, fmt.Errorf("text is required")
	}
	switch strings.TrimSpace(req.Source) {
	case "", "custom", "suggestion":
		req.Source = strings.TrimSpace(req.Source)
	default:
		return req, fmt.Errorf("source must be custom or suggestion")
	}
	return req, nil
}

func decodeJSONBody(r *http.Request, target any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(target); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func writeCoCreateResponse(w http.ResponseWriter, manifest ProjectManifest, session *ProjectSession, state webCoCreateState) {
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": session.Snapshot(),
		"cocreate": state,
		"running":  session.Snapshot().IsRunning,
	})
}

func writeCoCreateActionError(w http.ResponseWriter, err error, state webCoCreateState) {
	status := http.StatusConflict
	if errors.Is(err, ErrSessionActionInProgress) {
		status = http.StatusConflict
	} else if isBadCoCreateRequest(err) {
		status = http.StatusBadRequest
	}
	body := map[string]any{"error": err.Error()}
	if state.Kind != "" {
		body["cocreate"] = state
	}
	writeJSON(w, status, body)
}

func isBadCoCreateRequest(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "is required") ||
		strings.Contains(text, "must be one of") ||
		strings.Contains(text, "must be custom or suggestion") ||
		strings.Contains(text, "not found") ||
		strings.Contains(text, "not editable") ||
		strings.Contains(text, "not ready") ||
		strings.Contains(text, "non-negative integer") ||
		strings.Contains(text, "has not started")
}

func coCreateCommitLabel(kind string) string {
	switch kind {
	case webCoCreateKindStage:
		return "阶段方向已应用"
	case webCoCreateKindAdapt:
		return "改编已启动"
	default:
		return "创作已启动"
	}
}
