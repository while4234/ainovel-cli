package deai

import "slices"

// RepairBatch groups related deterministic findings into one bounded prose
// pass. A batch is guidance for the Writer, not an automated rewrite: the
// model still reads the cited text and supplies the replacement prose.
type RepairBatch struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	FindingCodes   []string `json:"finding_codes"`
	SuggestedEdits int      `json:"suggested_edits"`
	Instruction    string   `json:"instruction"`
}

// RepairPlan makes a failed de-AI audit actionable without treating a long
// chapter as an all-or-nothing rewrite. Attention findings are surfaced but
// remain non-blocking so a mechanical metric cannot overrule scene rhythm.
type RepairPlan struct {
	Mode       string        `json:"mode"`
	Batches    []RepairBatch `json:"batches,omitempty"`
	Attention  []Finding     `json:"attention,omitempty"`
	FinalCheck string        `json:"final_check"`
}

type repairBatchDefinition struct {
	id          string
	label       string
	codes       []string
	instruction string
}

var repairBatchDefinitions = []repairBatchDefinition{
	{
		id:          "format",
		label:       "格式清理",
		codes:       []string{"chapter_title_format", "markdown_subheading"},
		instruction: "把修订范围限定在标题和 Markdown 泄漏，正文情节、信息与段落顺序保持原样。",
	},
	{
		id:          "punctuation",
		label:       "标点节制",
		codes:       []string{"em_dash_overuse"},
		instruction: "逐处判断破折号承担的语气功能；解释性停顿按原段节奏落为自然断句、动作承接或换段，完成后保持相邻句群的呼吸。",
	},
	{
		id:          "expression",
		label:       "表达去模板化",
		codes:       []string{"triple_parallelism", "correction_sentence_overuse", "simile_overuse", "abstract_hedge_overuse"},
		instruction: "从命中处收紧失去功能的解释或装饰；需要保留的信息落到角色当下可见的事实、选择或后果，每处沿用本书声纹。",
	},
	{
		id:          "rhythm",
		label:       "叙述节奏",
		codes:       []string{"then_opener_overuse", "reaction_template_overuse", "repeated_sentence_opening"},
		instruction: "以命中句及相邻短段为单位，让动作、关系或环境承接转场，保留自然虚词、完整动作链和原有快慢节奏。",
	},
}

// RepairPlan returns bounded, category-by-category work for the current
// report. It never mandates a full rewrite. A Writer may choose a full
// chapter rewrite only after two non-improving batches or when story logic,
// causality, or scope also needs a structural repair.
func (r Report) RepairPlan() RepairPlan {
	plan := RepairPlan{
		Mode:       "batched",
		FinalCheck: "先完成 check_consistency、check_adaptation（如适用）要求的全部改稿。每完成一批先重新调用 check_de_ai；去AI化通过后重跑 check_consistency、check_adaptation。若任一后续检查又要求改稿，改完后必须重新 check_de_ai，直到同一版草稿的全部检查都通过，最后才 commit_chapter。连续两批未改善或剧情结构也失真时，才建议完整重写。每处在命中段及必要上下文内保留事实、人物声口、情绪强度、伏笔和自然句群；完成后连读相邻段，以信息完整、场景连续、人物可辨且仍像本书为修订标准。",
	}
	if r.Passed() {
		plan.Mode = "passed"
		return plan
	}

	for _, definition := range repairBatchDefinitions {
		batch := RepairBatch{
			ID:          definition.id,
			Label:       definition.label,
			Instruction: definition.instruction,
		}
		for _, finding := range r.Findings {
			if finding.Severity != SeverityRepair || !slices.Contains(definition.codes, finding.Code) {
				continue
			}
			batch.FindingCodes = append(batch.FindingCodes, finding.Code)
			batch.SuggestedEdits += suggestedEdits(finding)
		}
		if len(batch.FindingCodes) == 0 {
			continue
		}
		batch.SuggestedEdits = min(8, max(1, batch.SuggestedEdits))
		plan.Batches = append(plan.Batches, batch)
	}

	for _, finding := range r.Findings {
		if finding.Severity == SeverityAttention {
			plan.Attention = append(plan.Attention, finding)
		}
	}
	return plan
}

func suggestedEdits(finding Finding) int {
	if finding.Limit <= 0 {
		return 1
	}
	return max(1, finding.Actual-finding.Limit)
}
