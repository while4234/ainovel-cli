package startup

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/host"
)

// CoCreateSession 承载共创模式的非 UI 状态。
type CoCreateSession struct {
	history         []host.CoCreateMessage
	draftPrompt     string
	draftHistoryLen int
	ready           bool
	streamReply     string
	streamThinking  string
	suggestions     []string
}

type CoCreateSnapshot struct {
	History         []host.CoCreateMessage `json:"history"`
	DraftPrompt     string                 `json:"draft_prompt,omitempty"`
	DraftHistoryLen int                    `json:"draft_history_len,omitempty"`
	Ready           bool                   `json:"ready,omitempty"`
	StreamReply     string                 `json:"stream_reply,omitempty"`
	StreamThinking  string                 `json:"stream_thinking,omitempty"`
	Suggestions     []string               `json:"suggestions,omitempty"`
}

func NewCoCreateSession(initial string) *CoCreateSession {
	return &CoCreateSession{
		history: []host.CoCreateMessage{
			{Role: "user", Content: strings.TrimSpace(initial)},
		},
	}
}

func NewCoCreateSessionFromSnapshot(snapshot CoCreateSnapshot) *CoCreateSession {
	history := append([]host.CoCreateMessage(nil), snapshot.History...)
	draftPrompt := strings.TrimSpace(snapshot.DraftPrompt)
	draftHistoryLen := snapshot.DraftHistoryLen
	if latestDraft, latestLen := latestDraftFromHistory(history); latestDraft != "" && latestLen > draftHistoryLen {
		draftPrompt = latestDraft
		draftHistoryLen = latestLen
	}
	return &CoCreateSession{
		history:         history,
		draftPrompt:     draftPrompt,
		draftHistoryLen: draftHistoryLen,
		ready:           snapshot.Ready,
		streamReply:     strings.TrimSpace(snapshot.StreamReply),
		streamThinking:  strings.TrimSpace(snapshot.StreamThinking),
		suggestions:     append([]string(nil), snapshot.Suggestions...),
	}
}

func (s *CoCreateSession) Snapshot() CoCreateSnapshot {
	if s == nil {
		return CoCreateSnapshot{}
	}
	return CoCreateSnapshot{
		History:         append([]host.CoCreateMessage(nil), s.history...),
		DraftPrompt:     s.draftPrompt,
		DraftHistoryLen: s.draftHistoryLen,
		Ready:           s.ready,
		StreamReply:     s.streamReply,
		StreamThinking:  s.streamThinking,
		Suggestions:     append([]string(nil), s.suggestions...),
	}
}

func (s *CoCreateSession) History() []host.CoCreateMessage {
	if s == nil {
		return nil
	}
	return append([]host.CoCreateMessage(nil), s.history...)
}

func (s *CoCreateSession) ResetHistory(history []host.CoCreateMessage) {
	if s == nil {
		return
	}
	s.history = append([]host.CoCreateMessage(nil), history...)
	s.draftPrompt = ""
	s.draftHistoryLen = 0
	s.ready = false
	s.streamReply = ""
	s.streamThinking = ""
	s.suggestions = nil
}

func (s *CoCreateSession) ApplyReply(reply host.CoCreateReply) {
	if s == nil {
		return
	}
	s.streamReply = ""
	s.streamThinking = ""
	// history 里 assistant 存完整三段 Raw（含 [DRAFT]），下一轮模型才能看到
	// 自己上一轮写的草稿、在它基础上累积更新；只存 Message 会让 [DRAFT] 完全
	// 不进上下文，模型每轮只能凭对话重新归纳，早期细节容易丢。降级路径下
	// Raw == Message，等价。
	text := strings.TrimSpace(reply.Raw)
	if text == "" {
		text = strings.TrimSpace(reply.Message)
	}
	if text != "" {
		s.history = append(s.history, host.CoCreateMessage{Role: "assistant", Content: text})
	}
	// 仅当 Prompt 非空才覆盖 draft：parse 降级路径会返回 Prompt=""，此时
	// 必须保留上一轮 draft，否则用户已积累的"当前创作指令"会被截断的回复清空。
	if prompt := strings.TrimSpace(reply.Prompt); prompt != "" {
		s.draftPrompt = prompt
		s.draftHistoryLen = len(s.history)
	}
	s.ready = reply.Ready
	// suggestions 直接覆盖（包括覆盖为空）：每轮的引导只对当下有意义。
	s.suggestions = append(s.suggestions[:0], reply.Suggestions...)
}

func (s *CoCreateSession) AppendUser(text string) {
	if s == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	// 用户已经决定下一句要说什么，suggestions 立即作废，避免 AI 还没回复时
	// 旧建议挂在输入框上误导。
	s.suggestions = nil
	s.ready = false
	s.history = append(s.history, host.CoCreateMessage{Role: "user", Content: text})
}

// ApplyDelta 接收流式累积；kind="thinking" 写入推理流，"reply" 写入回复预览。
// 两路分别累积，UI 可分块染色显示，让用户在 thinking 阶段也看到 LLM 在工作。
func (s *CoCreateSession) ApplyDelta(kind, text string) {
	if s == nil {
		return
	}
	text = strings.TrimSpace(text)
	switch kind {
	case host.CoCreateProgressThinking:
		s.streamThinking = text
	case host.CoCreateProgressReply:
		s.streamReply = text
	}
}

func (s *CoCreateSession) StreamReply() string {
	if s == nil {
		return ""
	}
	return s.streamReply
}

func (s *CoCreateSession) StreamThinking() string {
	if s == nil {
		return ""
	}
	return s.streamThinking
}

func (s *CoCreateSession) DraftPrompt() string {
	if s == nil {
		return ""
	}
	return s.draftPrompt
}

func (s *CoCreateSession) DraftFresh() bool {
	if s == nil || strings.TrimSpace(s.draftPrompt) == "" {
		return false
	}
	return s.draftHistoryLen >= len(s.history)
}

func (s *CoCreateSession) DraftStale() bool {
	if s == nil || strings.TrimSpace(s.draftPrompt) == "" {
		return false
	}
	return s.draftHistoryLen < len(s.history)
}

func (s *CoCreateSession) Suggestions() []string {
	if s == nil {
		return nil
	}
	return s.suggestions
}

func (s *CoCreateSession) Ready() bool {
	if s == nil {
		return false
	}
	return s.ready
}

func (s *CoCreateSession) CanStart() bool {
	return s.DraftFresh()
}

func (s *CoCreateSession) InitialInput() string {
	if s == nil || len(s.history) == 0 {
		return ""
	}
	return strings.TrimSpace(s.history[0].Content)
}

func (s *CoCreateSession) BuildPlan() (Plan, error) {
	return s.BuildPlanWithWordBudget(0)
}

func (s *CoCreateSession) BuildPlanWithWordBudget(targetTotalWords int) (Plan, error) {
	if s == nil || !s.CanStart() {
		return Plan{}, fmt.Errorf("cocreate draft prompt is required")
	}
	draft := s.DraftPrompt()
	budget, err := wordBudgetForPrompt(targetTotalWords, draft)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		Mode:        ModeCoCreate,
		DisplayName: "共创规划",
		StartPrompt: host.BuildStartPromptWithBudget(draft, budget),
		RawPrompt:   draft,
		WordBudget:  budget,
	}, nil
}

func latestDraftFromHistory(history []host.CoCreateMessage) (string, int) {
	for idx := len(history) - 1; idx >= 0; idx-- {
		message := history[idx]
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		if draft := extractXMLTag(strings.TrimSpace(message.Content), "draft"); draft != "" {
			return draft, idx + 1
		}
	}
	return "", 0
}

func extractXMLTag(text, tag string) string {
	if text == "" || tag == "" {
		return ""
	}
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	start := strings.Index(text, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(text[start:], closeTag)
	if end < 0 {
		return strings.TrimSpace(text[start:])
	}
	return strings.TrimSpace(text[start : start+end])
}
