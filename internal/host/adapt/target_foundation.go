package adapt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func adaptationPlannerSystemPrompt(deps Deps) string {
	base := strings.TrimSpace(deps.Prompts.Planner)
	if deps.Store == nil {
		return base
	}
	target, err := deps.Store.Foundation.Load()
	if err != nil {
		return base
	}
	payload, err := json.Marshal(struct {
		Premise       string                         `json:"premise"`
		Characters    []domain.Character             `json:"confirmed_target_characters"`
		Relationships []domain.CharacterRelationship `json:"target_planned_relationships"`
		WorldRules    []domain.WorldRule             `json:"target_world_rules"`
	}{target.Premise, target.Characters, target.Relationships, target.WorldRules})
	if err != nil {
		return base
	}
	return base + "\n\nTARGET STORY FOUNDATION (confirmed canonical truth; never rename, delete, or replace confirmed cast):\n" + string(payload) +
		"\nKeep every planning claim explicitly separated as SOURCE FACT, TARGET ADAPTATION DECISION, or NEW ORIGINAL SETTING. SourceFoundation is read-only evidence; when it conflicts with this target foundation, the confirmed target decision governs the target story while the source discrepancy remains visible."
}

type TargetFoundationOptions struct {
	Brief                    string
	Feedback                 string
	ExpectedWorkflowRevision int
}

// GenerateTargetFoundation creates only target-story state. SourceFoundation
// remains immutable evidence and is never passed to a source write API here.
func GenerateTargetFoundation(_ context.Context, deps Deps, opts TargetFoundationOptions) (*domain.AdaptationFoundationReview, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	source, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil || source == nil {
		return nil, fmt.Errorf("load immutable source foundation: %w", err)
	}
	gate, err := deps.Store.CoreCast.LoadGateBinding()
	if err != nil || gate == nil {
		return nil, fmt.Errorf("load adaptation core cast gate: %w", err)
	}
	contract, err := deps.Store.CoreCast.RequireConfirmedGate(*gate, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("confirmed adaptation core cast is required: %w", err)
	}
	current, err := deps.Store.Foundation.Load()
	if err != nil {
		return nil, err
	}
	candidate := domain.CloneStoryFoundation(current)
	candidate.Characters = domain.ContractCharacters(contract)
	candidate.Relationships = append([]domain.CharacterRelationship(nil), contract.PlannedRelationships...)
	candidate.RelationshipsReviewed = false
	candidate.Premise = targetFoundationPremise(source.Premise, opts.Brief, opts.Feedback)
	candidate.WorldRules = targetFoundationWorldRules(source.WorldRules, opts.Feedback)
	return deps.Store.SaveAdaptationTargetFoundationCandidate(candidate, opts.ExpectedWorkflowRevision, opts.Brief, opts.Feedback)
}

func targetFoundationPremise(sourcePremise, brief, feedback string) string {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		brief = "按已确认改编意图创作目标作品"
	}
	var out strings.Builder
	out.WriteString("# 目标改编作品\n\n")
	out.WriteString("## 目标改编决策\n\n")
	out.WriteString(brief)
	if feedback = strings.TrimSpace(feedback); feedback != "" {
		out.WriteString("\n\n审核修订决策：")
		out.WriteString(feedback)
	}
	if sourcePremise = strings.TrimSpace(sourcePremise); sourcePremise != "" {
		out.WriteString("\n\n## 原著事实依据（只读）\n\n")
		out.WriteString(sourcePremise)
	}
	return out.String()
}

func targetFoundationWorldRules(sourceRules []domain.WorldRule, feedback string) []domain.WorldRule {
	rules := make([]domain.WorldRule, 0, len(sourceRules)+1)
	for _, source := range sourceRules {
		rule := source
		rule.ID = ""
		rule.Category = "source-preserved"
		rule.Boundary = strings.TrimSpace(strings.Join([]string{
			"原著事实（只读）", strings.TrimSpace(source.Boundary), "目标改编决策：保留；后续修改必须另建目标规则",
		}, "；"))
		rules = append(rules, rule)
	}
	decision := "原著未明确的信息只能作为目标创作决策或不确定项，不能标记为原著事实"
	if feedback = strings.TrimSpace(feedback); feedback != "" {
		decision += "；本轮审核决策：" + feedback
	}
	rules = append(rules, domain.WorldRule{
		Category: "target-decision", Title: "来源与目标边界", Rule: decision,
		Boundary: "目标作品规则；不写回 SourceFoundation", Strength: domain.WorldRuleStrengthHard,
	})
	return rules
}
