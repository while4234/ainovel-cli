package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const (
	webCoCreateKindNormal = "normal"
	webCoCreateKindStage  = "stage"
	webCoCreateKindAdapt  = "adapt"

	webCoCreateCheckpointVersion = 1

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
	briefing   *domain.AdaptationCoCreateBriefing
}

type webCoCreateSendRequest struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

type webCoCreateReviseRequest struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

type webCoCreateDecisionRequest struct {
	DecisionID   string `json:"decision_id"`
	OptionID     string `json:"option_id"`
	CustomAnswer string `json:"custom_answer"`
}

type webCoCreateMessage struct {
	ID           string `json:"id"`
	Role         string `json:"role"`
	Content      string `json:"content"`
	Editable     bool   `json:"editable,omitempty"`
	Source       string `json:"source,omitempty"`
	historyIndex int
}

type webCoCreateMessageCheckpoint struct {
	ID           string `json:"id"`
	Role         string `json:"role"`
	Content      string `json:"content"`
	Editable     bool   `json:"editable,omitempty"`
	Source       string `json:"source,omitempty"`
	HistoryIndex int    `json:"history_index"`
}

type webCoCreateCheckpoint struct {
	Version                int                                `json:"version"`
	UpdatedAt              time.Time                          `json:"updated_at"`
	Kind                   string                             `json:"kind"`
	Session                startup.CoCreateSnapshot           `json:"session"`
	Messages               []webCoCreateMessageCheckpoint     `json:"messages"`
	NextMessageSeq         int                                `json:"next_message_seq"`
	Failed                 bool                               `json:"failed,omitempty"`
	SourceFile             string                             `json:"source_file,omitempty"`
	SourcePath             string                             `json:"source_path,omitempty"`
	AdaptGranularity       string                             `json:"adapt_granularity,omitempty"`
	AdaptRewritePolicy     string                             `json:"adapt_rewrite_policy,omitempty"`
	AdaptWordTolerance     float64                            `json:"adapt_word_tolerance,omitempty"`
	TargetTotalWords       int                                `json:"target_total_words,omitempty"`
	AdaptationProposal     *domain.AdaptationPlan             `json:"adaptation_proposal,omitempty"`
	AdaptationVolumeReview *domain.AdaptationVolumeReview     `json:"adaptation_volume_review,omitempty"`
	DraftConsolidated      bool                               `json:"draft_consolidated_for_commit,omitempty"`
	AdaptationBriefing     *domain.AdaptationCoCreateBriefing `json:"adaptation_briefing,omitempty"`
}

type webCoCreateLogEntry struct {
	InputHistory      []host.CoCreateMessage `json:"input_history"`
	RawResponse       string                 `json:"raw_response"`
	Thinking          string                 `json:"thinking"`
	ParsedReply       string                 `json:"parsed_reply"`
	ParsedDraft       string                 `json:"parsed_draft"`
	ParsedReady       bool                   `json:"parsed_ready"`
	ParsedSuggestions []string               `json:"parsed_sugs"`
	Error             string                 `json:"error"`
}

type webCoCreateState struct {
	Kind             string                              `json:"kind"`
	Active           bool                                `json:"active"`
	Messages         []webCoCreateMessage                `json:"messages"`
	DraftPrompt      string                              `json:"draft_prompt"`
	Ready            bool                                `json:"ready"`
	Suggestions      []string                            `json:"suggestions"`
	StreamThinking   string                              `json:"stream_thinking,omitempty"`
	StreamReply      string                              `json:"stream_reply,omitempty"`
	AdaptMode        string                              `json:"adapt_mode,omitempty"`
	RewritePolicy    string                              `json:"rewrite_policy,omitempty"`
	WordTolerance    float64                             `json:"word_tolerance,omitempty"`
	TargetTotalWords int                                 `json:"target_total_words,omitempty"`
	SourceFile       string                              `json:"source_file,omitempty"`
	Proposal         *domain.AdaptationPlan              `json:"proposal,omitempty"`
	VolumeReview     *domain.AdaptationVolumeReview      `json:"volume_review,omitempty"`
	CanStart         bool                                `json:"can_start"`
	ModeLocked       bool                                `json:"mode_locked,omitempty"`
	CommittedLabel   string                              `json:"committed_label,omitempty"`
	Briefing         *webCoCreateBriefingState           `json:"briefing,omitempty"`
	PendingDecisions []domain.AdaptationBriefingDecision `json:"pending_decisions,omitempty"`
	BlockedReason    string                              `json:"blocked_reason,omitempty"`
}

type webCoCreateSession struct {
	kind                   string
	session                *startup.CoCreateSession
	messages               []webCoCreateMessage
	nextMessageSeq         int
	failed                 bool
	sourceFile             string
	sourcePath             string
	adaptGranularity       string
	adaptRewritePolicy     string
	adaptWordTolerance     float64
	targetTotalWords       int
	adaptationProposal     *domain.AdaptationPlan
	adaptationVolumeReview *domain.AdaptationVolumeReview
	draftConsolidated      bool
	adaptationBriefing     *domain.AdaptationCoCreateBriefing
}

type webCoCreateBriefingState struct {
	Active                bool   `json:"active"`
	TriggerReason         string `json:"trigger_reason,omitempty"`
	PendingDecisionCount  int    `json:"pending_decision_count"`
	ResolvedDecisionCount int    `json:"resolved_decision_count"`
	TotalDecisionCount    int    `json:"total_decision_count"`
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
		req.Tolerance = startup.AdaptationWordToleranceForGranularity(mode, req.Tolerance)
		if rewritePolicy == "" {
			writeError(w, http.StatusBadRequest, "adaptation rewrite policy is required")
			return
		}
		st := storepkg.NewStore(manifest.OutputDir)
		if _, _, err := adapt.ValidatePreparedSource(st, sourcePath); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		current, err := st.Adaptation.CoCreateDossierCurrent(adapt.CoCreateDossierPromptVersion, adapt.CoCreateDossierBatchSize)
		if err != nil {
			writeError(w, http.StatusConflict, "read adaptation co-create dossier: "+err.Error())
			return
		}
		if !current {
			writeError(w, http.StatusConflict, "adaptation co-create dossier missing or stale; run source analysis first")
			return
		}
		intent := adapt.BuildCoCreateIntent(coCreateAdaptIntentRaw(req.Initial), mode, rewritePolicy, req.Tolerance)
		briefing, err := session.host.EnsureAdaptationCoCreateBriefing(r.Context(), sourcePath, intent)
		if err != nil {
			writeError(w, http.StatusConflict, "prepare adaptation co-create briefing: "+err.Error())
			return
		}
		req.briefing = briefing
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

func (s *Server) handleProjectCoCreateDecision(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req webCoCreateDecisionRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid co-create decision request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.ResolveCoCreateDecision(r.Context(), req)
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
	state, err := session.CommitCoCreate(r.Context())
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
		rewritePolicy := domain.AdaptationRewritePolicyForGranularity(granularity)
		tolerance := startup.AdaptationWordToleranceForGranularity(granularity, req.Tolerance)
		opener := adaptCoCreateOpener(granularity, rewritePolicy, tolerance)
		state := &webCoCreateSession{
			kind:               kind,
			session:            startup.NewCoCreateSession(opener),
			sourceFile:         strings.TrimSpace(req.SourceFile),
			sourcePath:         strings.TrimSpace(req.sourcePath),
			adaptGranularity:   granularity,
			adaptRewritePolicy: rewritePolicy,
			adaptWordTolerance: tolerance,
			adaptationBriefing: req.briefing,
		}
		state.messages = []webCoCreateMessage{state.newMessage("system", adaptCoCreateSystemLine, "", -1)}
		if initial := strings.TrimSpace(req.Initial); initial != "" {
			historyIndex := len(state.session.History())
			state.session.AppendUser(initial)
			state.messages = append(state.messages, state.newMessage("user", initial, "custom", historyIndex))
		}
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

func (s *webCoCreateSession) checkpoint(now time.Time) webCoCreateCheckpoint {
	if s == nil {
		return webCoCreateCheckpoint{}
	}
	messages := make([]webCoCreateMessageCheckpoint, 0, len(s.messages))
	for _, message := range s.messages {
		messages = append(messages, webCoCreateMessageCheckpoint{
			ID:           message.ID,
			Role:         message.Role,
			Content:      message.Content,
			Editable:     message.Editable,
			Source:       message.Source,
			HistoryIndex: message.historyIndex,
		})
	}
	return webCoCreateCheckpoint{
		Version:                webCoCreateCheckpointVersion,
		UpdatedAt:              now.UTC(),
		Kind:                   s.kind,
		Session:                s.session.Snapshot(),
		Messages:               messages,
		NextMessageSeq:         s.nextMessageSeq,
		Failed:                 s.failed,
		SourceFile:             s.sourceFile,
		SourcePath:             s.sourcePath,
		AdaptGranularity:       s.adaptGranularity,
		AdaptRewritePolicy:     s.adaptRewritePolicy,
		AdaptWordTolerance:     s.adaptWordTolerance,
		TargetTotalWords:       s.targetTotalWords,
		AdaptationProposal:     s.adaptationProposal,
		AdaptationVolumeReview: s.adaptationVolumeReview,
		DraftConsolidated:      s.draftConsolidated,
		AdaptationBriefing:     s.adaptationBriefing,
	}
}

func webCoCreateSessionFromCheckpoint(checkpoint webCoCreateCheckpoint) (*webCoCreateSession, error) {
	if checkpoint.Version != webCoCreateCheckpointVersion {
		return nil, fmt.Errorf("unsupported co-create checkpoint version %d", checkpoint.Version)
	}
	kind := strings.TrimSpace(checkpoint.Kind)
	if kind == "" {
		kind = webCoCreateKindNormal
	}
	switch kind {
	case webCoCreateKindNormal, webCoCreateKindStage, webCoCreateKindAdapt:
	default:
		return nil, fmt.Errorf("unsupported co-create kind %q", checkpoint.Kind)
	}
	if len(checkpoint.Session.History) == 0 {
		return nil, fmt.Errorf("co-create checkpoint history is empty")
	}
	messages := make([]webCoCreateMessage, 0, len(checkpoint.Messages))
	for _, message := range checkpoint.Messages {
		messages = append(messages, webCoCreateMessage{
			ID:           message.ID,
			Role:         message.Role,
			Content:      message.Content,
			Editable:     message.Editable,
			Source:       message.Source,
			historyIndex: message.HistoryIndex,
		})
	}
	nextMessageSeq := checkpoint.NextMessageSeq
	if nextMessageSeq < len(messages) {
		nextMessageSeq = len(messages)
	}
	adaptGranularity, adaptRewritePolicy, adaptWordTolerance := checkpoint.AdaptGranularity, checkpoint.AdaptRewritePolicy, checkpoint.AdaptWordTolerance
	if kind == webCoCreateKindAdapt {
		adaptGranularity, adaptRewritePolicy, adaptWordTolerance = coCreateCheckpointAdaptOptions(checkpoint)
	}
	return &webCoCreateSession{
		kind:                   kind,
		session:                startup.NewCoCreateSessionFromSnapshot(checkpoint.Session),
		messages:               messages,
		nextMessageSeq:         nextMessageSeq,
		failed:                 checkpoint.Failed,
		sourceFile:             strings.TrimSpace(checkpoint.SourceFile),
		sourcePath:             strings.TrimSpace(checkpoint.SourcePath),
		adaptGranularity:       adaptGranularity,
		adaptRewritePolicy:     adaptRewritePolicy,
		adaptWordTolerance:     adaptWordTolerance,
		targetTotalWords:       checkpoint.TargetTotalWords,
		adaptationProposal:     checkpoint.AdaptationProposal,
		adaptationVolumeReview: checkpoint.AdaptationVolumeReview,
		draftConsolidated:      checkpoint.DraftConsolidated,
		adaptationBriefing:     checkpoint.AdaptationBriefing,
	}, nil
}

func coCreateCheckpointAdaptOptions(checkpoint webCoCreateCheckpoint) (string, string, float64) {
	granularity := strings.TrimSpace(checkpoint.AdaptGranularity)
	rewritePolicy := strings.TrimSpace(checkpoint.AdaptRewritePolicy)
	wordTolerance := checkpoint.AdaptWordTolerance
	if (granularity == "" || rewritePolicy == "") && len(checkpoint.Session.History) > 0 {
		logGranularity, logRewritePolicy, logWordTolerance := coCreateLogAdaptOptions(checkpoint.Session.History[0].Content)
		if granularity == "" {
			granularity = logGranularity
		}
		if rewritePolicy == "" {
			rewritePolicy = logRewritePolicy
		}
		if wordTolerance <= 0 {
			wordTolerance = logWordTolerance
		}
	}
	return normalizeWebAdaptCoCreateOptions(granularity, rewritePolicy, wordTolerance)
}

func webCoCreateSessionFromLogEntry(entry webCoCreateLogEntry) (*webCoCreateSession, error) {
	history := cleanCoCreateLogHistory(entry.InputHistory)
	if len(history) == 0 {
		return nil, fmt.Errorf("co-create log history is empty")
	}
	kind := inferWebCoCreateKindFromLog(history)
	draftPrompt, ready, suggestions := coCreateLogDraftState(entry, history)
	sessionHistory := append([]host.CoCreateMessage(nil), history...)
	assistantMessage := strings.TrimSpace(entry.ParsedReply)
	if assistantMessage == "" {
		assistantMessage = extractCoCreateLogTag(entry.RawResponse, "reply")
	}
	if strings.TrimSpace(entry.Error) == "" {
		raw := strings.TrimSpace(entry.RawResponse)
		if raw == "" {
			raw = assistantMessage
		}
		if raw != "" {
			sessionHistory = append(sessionHistory, host.CoCreateMessage{Role: "assistant", Content: raw})
		}
	}
	state := &webCoCreateSession{
		kind:    kind,
		session: startup.NewCoCreateSessionFromSnapshot(startup.CoCreateSnapshot{History: sessionHistory, DraftPrompt: draftPrompt, Ready: ready, Suggestions: suggestions}),
		failed:  strings.TrimSpace(entry.Error) != "",
	}
	if kind == webCoCreateKindAdapt {
		state.adaptGranularity, state.adaptRewritePolicy, state.adaptWordTolerance = coCreateLogAdaptOptions(history[0].Content)
	}
	state.messages = webCoCreateMessagesFromLog(kind, history, assistantMessage, strings.TrimSpace(entry.Error) == "")
	return state, nil
}

func cleanCoCreateLogHistory(history []host.CoCreateMessage) []host.CoCreateMessage {
	out := make([]host.CoCreateMessage, 0, len(history))
	for _, message := range history {
		role := strings.TrimSpace(message.Role)
		content := strings.TrimSpace(message.Content)
		if role == "" || content == "" {
			continue
		}
		switch role {
		case "user", "assistant", "system":
			out = append(out, host.CoCreateMessage{Role: role, Content: content})
		}
	}
	return out
}

func inferWebCoCreateKindFromLog(history []host.CoCreateMessage) string {
	if len(history) == 0 {
		return webCoCreateKindNormal
	}
	first := history[0].Content
	if strings.Contains(first, "granularity=") && strings.Contains(first, "rewrite_policy=") {
		return webCoCreateKindAdapt
	}
	if strings.TrimSpace(first) == strings.TrimSpace(stageCoCreateOpener) {
		return webCoCreateKindStage
	}
	return webCoCreateKindNormal
}

func coCreateLogAdaptOptions(opener string) (string, string, float64) {
	granularity := strings.TrimSpace(coCreateLogKey(opener, "granularity"))
	if normalized, ok := domain.StrictAdaptationGranularity(granularity); ok {
		granularity = normalized
	} else {
		granularity = domain.AdaptationGranularityChapter
	}
	rewritePolicy := strings.TrimSpace(coCreateLogKey(opener, "rewrite_policy"))
	if rewritePolicy == "" {
		rewritePolicy = domain.AdaptationRewritePolicyForGranularity(granularity)
	}
	tolerance := adapt.DefaultWordTolerance
	if raw := strings.TrimSpace(coCreateLogKey(opener, "word_tolerance")); raw != "" && raw != "disabled" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed > 0 {
			tolerance = parsed
		}
	}
	return normalizeWebAdaptCoCreateOptions(granularity, rewritePolicy, tolerance)
}

func coCreateLogKey(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		left, right, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(left) != key {
			continue
		}
		return strings.TrimSpace(right)
	}
	return ""
}

func coCreateLogDraftState(entry webCoCreateLogEntry, history []host.CoCreateMessage) (string, bool, []string) {
	draft := strings.TrimSpace(entry.ParsedDraft)
	ready := entry.ParsedReady
	suggestions := append([]string(nil), entry.ParsedSuggestions...)
	if draft != "" {
		return draft, ready, suggestions
	}
	for idx := len(history) - 1; idx >= 0; idx-- {
		if history[idx].Role != "assistant" {
			continue
		}
		draft = extractCoCreateLogTag(history[idx].Content, "draft")
		if draft == "" {
			continue
		}
		ready = strings.EqualFold(strings.TrimSpace(extractCoCreateLogTag(history[idx].Content, "ready")), "true")
		if len(suggestions) == 0 && len(history) > 0 && history[len(history)-1].Role == "assistant" {
			suggestions = splitCoCreateLogSuggestions(extractCoCreateLogTag(history[idx].Content, "suggestions"))
		}
		return draft, ready, suggestions
	}
	return draft, ready, suggestions
}

func splitCoCreateLogSuggestions(text string) []string {
	var suggestions []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-* "))
		if line != "" {
			suggestions = append(suggestions, line)
		}
	}
	return suggestions
}

func extractCoCreateLogTag(text, tag string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(text, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(text[start:], close)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[start : start+end])
}

func webCoCreateMessagesFromLog(kind string, history []host.CoCreateMessage, assistantMessage string, appendAssistant bool) []webCoCreateMessage {
	state := &webCoCreateSession{kind: kind}
	if kind == webCoCreateKindAdapt {
		state.messages = append(state.messages, state.newMessage("system", adaptCoCreateSystemLine, "", -1))
	}
	if kind == webCoCreateKindStage {
		state.messages = append(state.messages, state.newMessage("system", stageCoCreateSystemLine, "", -1))
	}
	for idx, message := range history {
		if coCreateLogMessageHidden(kind, idx, message) {
			continue
		}
		content := message.Content
		source := ""
		if message.Role == "assistant" {
			if reply := extractCoCreateLogTag(content, "reply"); reply != "" {
				content = reply
			}
		} else if message.Role == "user" {
			source = "custom"
		}
		state.messages = append(state.messages, state.newMessage(message.Role, content, source, idx))
	}
	if appendAssistant && strings.TrimSpace(assistantMessage) != "" {
		state.messages = append(state.messages, state.newMessage("assistant", assistantMessage, "", len(history)))
	}
	return state.messages
}

func coCreateLogMessageHidden(kind string, index int, message host.CoCreateMessage) bool {
	if index != 0 || message.Role != "user" {
		return false
	}
	switch kind {
	case webCoCreateKindAdapt:
		return strings.Contains(message.Content, "granularity=") && strings.Contains(message.Content, "rewrite_policy=")
	case webCoCreateKindStage:
		return strings.TrimSpace(message.Content) == strings.TrimSpace(stageCoCreateOpener)
	default:
		return false
	}
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
	s.draftConsolidated = false
	if coCreateMessageSource(source) == "" {
		source = "custom"
	}
	s.messages = append(s.messages, s.newMessage("user", text, source, historyIndex))
	return nil
}

func (s *webCoCreateSession) applyReply(reply host.CoCreateReply) {
	historyIndex := len(s.session.History())
	s.session.ApplyReply(reply)
	s.draftConsolidated = false
	if text := strings.TrimSpace(reply.Message); text != "" {
		s.messages = append(s.messages, s.newMessage("assistant", text, "", historyIndex))
	}
}

func (s *webCoCreateSession) rollbackDraftAfterRejectedReply(reply host.CoCreateReply) {
	if s == nil || s.session == nil {
		return
	}
	historyIndex := len(s.session.History())
	s.session.ApplyReply(host.CoCreateReply{})
	s.draftConsolidated = false
	if text := strings.TrimSpace(reply.Message); text != "" {
		s.messages = append(s.messages, s.newMessage("assistant", text, "", historyIndex))
	}
}

func (s *webCoCreateSession) draftNeedsRepair(reply host.CoCreateReply, previousDraft string) bool {
	if s == nil || s.session == nil {
		return false
	}
	prompt := strings.TrimSpace(reply.Prompt)
	if prompt == "" {
		return true
	}
	if draftPromptRegressed(previousDraft, prompt) {
		return true
	}
	if strings.TrimSpace(previousDraft) != "" && prompt == strings.TrimSpace(previousDraft) && s.session.DraftStale() {
		return true
	}
	return false
}

func (s *webCoCreateSession) currentDraftNeedsRepair() (bool, string) {
	if s == nil || s.session == nil {
		return false, ""
	}
	currentDraft := s.draftPrompt()
	if strings.TrimSpace(currentDraft) == "" {
		return false, ""
	}
	baseDraft := s.previousStableDraft(currentDraft)
	if s.latestHistoryDraftNeedsRepair(currentDraft) {
		return true, baseDraft
	}
	if containsDraftOmissionPlaceholder(currentDraft) {
		return true, baseDraft
	}
	if baseDraft != "" && draftPromptRegressed(baseDraft, currentDraft) {
		return true, baseDraft
	}
	return false, currentDraft
}

func (s *webCoCreateSession) shouldConsolidateDraftBeforeCommit() bool {
	if s == nil || s.session == nil || s.kind != webCoCreateKindAdapt || !s.session.DraftFresh() {
		return false
	}
	if s.draftConsolidated {
		return false
	}
	return coCreatePlanningUserMessageCount(s.kind, s.session.History()) > 1
}

func coCreatePlanningUserMessageCount(kind string, history []host.CoCreateMessage) int {
	count := 0
	start := 0
	if len(history) > 0 && coCreateLogMessageHidden(kind, 0, history[0]) {
		start = 1
	}
	for idx := start; idx < len(history); idx++ {
		message := history[idx]
		if strings.TrimSpace(message.Role) != "user" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		count++
	}
	return count
}

func (s *webCoCreateSession) latestHistoryDraftNeedsRepair(currentDraft string) bool {
	if s == nil || s.session == nil {
		return false
	}
	history := s.session.History()
	for idx := len(history) - 1; idx >= 0; idx-- {
		message := history[idx]
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		draft := extractCoCreateLogTag(message.Content, "draft")
		if draft == "" {
			continue
		}
		if containsDraftOmissionPlaceholder(draft) {
			return true
		}
		baseDraft := previousStableDraftInHistory(history[:idx], currentDraft)
		return baseDraft != "" && draftPromptRegressed(baseDraft, draft)
	}
	return false
}

func (s *webCoCreateSession) previousStableDraft(currentDraft string) string {
	if s == nil || s.session == nil {
		return ""
	}
	currentDraft = strings.TrimSpace(currentDraft)
	return previousStableDraftInHistory(s.session.History(), currentDraft)
}

func previousStableDraftInHistory(history []host.CoCreateMessage, currentDraft string) string {
	currentDraft = strings.TrimSpace(currentDraft)
	skippedCurrent := currentDraft == ""
	for idx := len(history) - 1; idx >= 0; idx-- {
		message := history[idx]
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		draft := extractCoCreateLogTag(message.Content, "draft")
		if draft == "" {
			continue
		}
		if !skippedCurrent && strings.TrimSpace(draft) == currentDraft {
			skippedCurrent = true
			continue
		}
		if !containsDraftOmissionPlaceholder(draft) {
			return draft
		}
	}
	if containsDraftOmissionPlaceholder(currentDraft) {
		return ""
	}
	return currentDraft
}

func draftPromptRegressed(previousDraft, nextDraft string) bool {
	previousDraft = strings.TrimSpace(previousDraft)
	nextDraft = strings.TrimSpace(nextDraft)
	if previousDraft == "" || nextDraft == "" {
		return false
	}
	if containsDraftOmissionPlaceholder(nextDraft) {
		return true
	}
	previousLen := len([]rune(previousDraft))
	nextLen := len([]rune(nextDraft))
	return previousLen >= 1200 && nextLen < previousLen*2/3
}

func containsDraftOmissionPlaceholder(draft string) bool {
	draft = strings.ToLower(strings.TrimSpace(draft))
	if draft == "" {
		return false
	}
	placeholders := []string{
		"同上",
		"同前轮",
		"同上一轮",
		"如上",
		"前述",
		"前文",
		"前面",
		"前轮",
		"上一轮",
		"上轮",
		"上一稿",
		"上一版",
		"前一稿",
		"前一版",
		"此前",
		"已完整记录",
		"不再重复",
		"不赘述",
		"见上",
		"沿用上一轮",
		"沿用上轮",
		"沿用上一稿",
		"沿用上一版",
		"保持上一轮",
		"保持上轮",
		"保持上一稿",
		"保持上一版",
		"保留上一轮",
		"保留上轮",
		"保留上一稿",
		"保留上一版",
		"previous round",
		"previous draft",
		"last round",
		"last draft",
		"same as above",
		"as above",
	}
	for _, placeholder := range placeholders {
		if strings.Contains(draft, placeholder) {
			return true
		}
	}
	return false
}

func (s *webCoCreateSession) draftRepairHistory(reply host.CoCreateReply, previousDraft string) []host.CoCreateMessage {
	if s == nil || s.session == nil {
		return nil
	}
	history := compactDraftRepairHistory(s.session.History(), previousDraft)
	if raw := strings.TrimSpace(reply.Raw); raw != "" {
		history = append(history, host.CoCreateMessage{Role: "assistant", Content: raw})
	} else if message := strings.TrimSpace(reply.Message); message != "" {
		history = append(history, host.CoCreateMessage{Role: "assistant", Content: message})
	}
	history = append(history, host.CoCreateMessage{Role: "user", Content: coCreateDraftRepairInstruction(s.kind, previousDraft)})
	return history
}

func compactDraftRepairHistory(history []host.CoCreateMessage, previousDraft string) []host.CoCreateMessage {
	out := make([]host.CoCreateMessage, 0, len(history)+2)
	if len(history) > 0 {
		out = append(out, history[0])
	}
	if previousDraft = strings.TrimSpace(previousDraft); previousDraft != "" {
		out = append(out, host.CoCreateMessage{
			Role:    "assistant",
			Content: "<draft>\n" + previousDraft + "\n</draft>",
		})
	}
	recentAssistant := recentNonDraftAssistantIndexes(history, 3)
	for idx := 1; idx < len(history); idx++ {
		message := history[idx]
		role := strings.TrimSpace(message.Role)
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		if role == "user" {
			out = append(out, message)
			continue
		}
		if _, ok := recentAssistant[idx]; ok {
			out = append(out, message)
		}
	}
	return out
}

func recentNonDraftAssistantIndexes(history []host.CoCreateMessage, limit int) map[int]struct{} {
	out := make(map[int]struct{})
	if limit <= 0 {
		return out
	}
	for idx := len(history) - 1; idx >= 1 && len(out) < limit; idx-- {
		message := history[idx]
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" || extractCoCreateLogTag(content, "draft") != "" {
			continue
		}
		out[idx] = struct{}{}
	}
	return out
}

func coCreateDraftRepairInstruction(kind string, previousDraft string) string {
	instruction := `Internal draft consolidation request:
The previous response did not produce a stable, complete <draft>, or the draft did not absorb the latest user discussion.
Update the previous stable draft with all confirmed planning decisions from the user messages above, especially turns after the previous stable draft, then return the normal four XML tags: <reply>, <draft>, <ready>, <suggestions>.
Do not paste the chat transcript into <draft>. Distill confirmed decisions into one executable Markdown draft.
The <draft> must be complete, current, and preserve the previous stable draft while adding the latest confirmed decisions.
If required decisions are still missing, write the known decisions plus a "pending decisions" section in <draft> and set <ready>false</ready>.
Never use placeholders such as "same as above", "同上", "同前轮", "上一轮", "已完整记录", "完整保留", "不再重复", or "见上"; the draft must be self-contained.`
	if kind == webCoCreateKindAdapt {
		instruction += `
For adaptation co-create, preserve the selected granularity, rewrite_policy, and word_tolerance exactly as originally provided. Do not ask the user to choose chapter/arc/free again.`
	}
	if previousDraft = strings.TrimSpace(previousDraft); previousDraft != "" {
		instruction += "\n\nPrevious stable draft to preserve and merge:\n<previous_draft>\n" + previousDraft + "\n</previous_draft>"
	}
	return strings.TrimSpace(instruction)
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
		s.draftConsolidated = false
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
	if s.session == nil || !s.session.DraftFresh() {
		return fmt.Errorf("co-create draft is not up to date; continue co-create until the latest discussion is consolidated")
	}
	return nil
}

func (s *webCoCreateSession) draftPrompt() string {
	if s == nil || s.session == nil {
		return ""
	}
	return strings.TrimSpace(s.session.DraftPrompt())
}

func (s *webCoCreateSession) pendingBriefingDecisions() []domain.AdaptationBriefingDecision {
	if s == nil || s.kind != webCoCreateKindAdapt {
		return nil
	}
	return adapt.PendingCoCreateBriefingDecisions(s.adaptationBriefing)
}

func (s *webCoCreateSession) hasPendingBriefingDecisions() bool {
	return len(s.pendingBriefingDecisions()) > 0
}

func (s *webCoCreateSession) apiState() webCoCreateState {
	if s == nil {
		return webCoCreateState{}
	}
	canStart := s.session.CanStart()
	if needsRepair, _ := s.currentDraftNeedsRepair(); needsRepair {
		canStart = false
	}
	pendingDecisions := s.pendingBriefingDecisions()
	briefingState := coCreateBriefingState(s.adaptationBriefing)
	blockedReason := ""
	if len(pendingDecisions) > 0 {
		canStart = false
		blockedReason = "resolve adaptation briefing decisions before draft generation"
		if len(pendingDecisions) > 4 {
			pendingDecisions = append([]domain.AdaptationBriefingDecision(nil), pendingDecisions[:4]...)
		}
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
		Proposal:         s.adaptationProposal,
		VolumeReview:     s.adaptationVolumeReview,
		CanStart:         canStart,
		ModeLocked:       s.kind == webCoCreateKindAdapt,
		Briefing:         briefingState,
		PendingDecisions: pendingDecisions,
		BlockedReason:    blockedReason,
	}
}

func coCreateBriefingState(briefing *domain.AdaptationCoCreateBriefing) *webCoCreateBriefingState {
	if briefing == nil {
		return nil
	}
	return &webCoCreateBriefingState{
		Active:                true,
		TriggerReason:         briefing.TriggerReason,
		PendingDecisionCount:  len(adapt.PendingCoCreateBriefingDecisions(briefing)),
		ResolvedDecisionCount: len(briefing.ResolvedDecisions),
		TotalDecisionCount:    len(briefing.Decisions),
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
	_ = rewritePolicy
	modeContract := startup.AdaptationModeContract(granularity, wordTolerance)
	return strings.TrimSpace(fmt.Sprintf(`我想基于这本小说做改编，已确认改编模式如下：

%s

请基于原书分析帮我确认具体改编目标。只围绕上面的当前模式整理 brief，不要写入其它模式的规则，也不要再询问或改动 chapter/arc/free 与 full_rewrite/preserve_details 这两个模式选择。`,
		modeContract))
}

func coCreateAdaptIntentRaw(initial string) string {
	initial = strings.TrimSpace(initial)
	if initial != "" {
		return initial
	}
	return "基于原书分析确认具体改编目标。"
}

func normalizeWebAdaptCoCreateOptions(granularity, rewritePolicy string, wordTolerance float64) (string, string, float64) {
	normalizedGranularity, ok := domain.StrictAdaptationGranularity(strings.TrimSpace(granularity))
	if !ok {
		normalizedGranularity = domain.AdaptationGranularityChapter
	}
	normalizedRewritePolicy := strings.TrimSpace(rewritePolicy)
	if normalizedRewritePolicy == "" {
		normalizedRewritePolicy = domain.AdaptationRewritePolicyForGranularity(normalizedGranularity)
	}
	return normalizedGranularity, normalizedRewritePolicy, startup.AdaptationWordToleranceForGranularity(normalizedGranularity, wordTolerance)
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
	if kind == webCoCreateKindAdapt {
		return "改编提案已生成"
	}
	switch kind {
	case webCoCreateKindStage:
		return "阶段方向已应用"
	case webCoCreateKindAdapt:
		return "改编已启动"
	default:
		return "创作已启动"
	}
}
