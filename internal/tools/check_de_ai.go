package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/deai"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CheckDeAITool is the explicit post-writing prose stage. It is intentionally
// separate from consistency/adaptation checks: those validate story facts and
// source obligations, while this tool validates export cleanliness and the
// recurrent prose symptoms observed across long-running generated novels.
type CheckDeAITool struct{ store *store.Store }

type checkDeAIResult struct {
	deai.Audit
	RepairPlan deai.RepairPlan `json:"repair_plan"`
}

func NewCheckDeAITool(store *store.Store) *CheckDeAITool { return &CheckDeAITool{store: store} }

func (t *CheckDeAITool) Name() string { return "check_de_ai" }
func (t *CheckDeAITool) Description() string {
	return "独立去AI化审校：读取当前草稿，检查正文标题泄漏、破折号、排比、模板反应、比喻和叙述缓冲词；返回可直接定位的原文 examples，修改后必须重新调用。"
}
func (t *CheckDeAITool) Label() string                          { return "去AI化审校" }
func (t *CheckDeAITool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *CheckDeAITool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *CheckDeAITool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("要做去AI化审校的章节号")).Required(),
	)
}

func (t *CheckDeAITool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Chapter int `json:"chapter"`
	}
	if err := json.Unmarshal(args, &request); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if request.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if err := t.store.DeAI.Enable(); err != nil {
		return nil, fmt.Errorf("enable de-AI stage: %w: %w", errs.ErrStoreWrite, err)
	}
	content, _, err := t.store.Drafts.LoadChapterContent(request.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load chapter content: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", request.Chapter, errs.ErrToolPrecondition)
	}

	report := deai.Analyze(content)
	audit := deai.Audit{
		Version:     deai.PolicyVersion,
		Chapter:     request.Chapter,
		DraftSHA256: store.TextSHA256(content),
		Passed:      report.Passed(),
		Report:      report,
		CheckedAt:   time.Now(),
	}
	if err := t.store.DeAI.SaveAudit(audit); err != nil {
		return nil, fmt.Errorf("save de-AI audit: %w: %w", errs.ErrStoreWrite, err)
	}
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(request.Chapter), "de_ai_check", t.store.DeAI.AuditPath(request.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint de-AI audit: %w: %w", errs.ErrStoreWrite, err)
	}
	return json.Marshal(checkDeAIResult{
		Audit:      audit,
		RepairPlan: report.RepairPlan(),
	})
}
