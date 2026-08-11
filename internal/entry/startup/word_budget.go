package startup

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func wordBudgetForPrompt(targetTotalWords int, prompt string) (*domain.WordBudget, error) {
	if targetTotalWords < 0 {
		return nil, fmt.Errorf("target_total_words must be a non-negative integer")
	}
	if targetTotalWords > 0 {
		budget, _ := domain.NewWordBudgetFromTarget(targetTotalWords, domain.WordBudgetSourceAPI)
		return applyRequestedChapterCount(budget, prompt), nil
	}
	if budget, ok := domain.ParseWordBudgetFromText(prompt, domain.WordBudgetSourcePrompt); ok {
		return applyRequestedChapterCount(budget, prompt), nil
	}
	return nil, nil
}

func applyRequestedChapterCount(budget *domain.WordBudget, prompt string) *domain.WordBudget {
	if budget == nil {
		return nil
	}
	chapters, ok := domain.ParseRequestedChapterCount(prompt)
	if !ok {
		return budget
	}
	updated := budget.WithRequestedChapters(chapters)
	return &updated
}
