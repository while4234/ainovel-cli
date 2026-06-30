package startup

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

// PrepareAdaptNovel turns a confirmed adaptation brief into a startup plan.
func PrepareAdaptNovel(req Request) (Plan, error) {
	brief := strings.TrimSpace(req.UserPrompt)
	if brief == "" {
		return Plan{}, fmt.Errorf("adaptation brief is required")
	}
	if strings.TrimSpace(req.NovelPath) == "" {
		return Plan{}, fmt.Errorf("novel path is required")
	}
	granularity := domain.NormalizeAdaptationGranularity(req.AdaptGranularity)
	rewritePolicy := domain.NormalizeAdaptationRewritePolicy(req.AdaptRewritePolicy)
	wordTolerance := req.AdaptWordTolerance
	if wordTolerance <= 0 {
		wordTolerance = adapt.DefaultWordTolerance
	}
	return Plan{
		Mode:               ModeAdaptNovel,
		DisplayName:        "小说改编",
		RawPrompt:          brief,
		AdaptGranularity:   granularity,
		AdaptRewritePolicy: rewritePolicy,
		AdaptWordTolerance: wordTolerance,
	}, nil
}
