package startup

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

const (
	DefaultAdaptationGranularity   = domain.AdaptationGranularityChapter
	DefaultAdaptationRewritePolicy = domain.AdaptationRewritePreserveDetails
	DefaultAdaptationWordTolerance = adapt.DefaultWordTolerance
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
	granularity := normalizeAdaptationGranularity(req.AdaptGranularity)
	rewritePolicy := domain.AdaptationRewritePolicyForGranularity(granularity)
	wordTolerance := req.AdaptWordTolerance
	if wordTolerance <= 0 {
		wordTolerance = DefaultAdaptationWordTolerance
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

func DefaultAdaptationBrief(granularity, rewritePolicy string, wordTolerance float64) string {
	granularity = normalizeAdaptationGranularity(granularity)
	rewritePolicy = domain.AdaptationRewritePolicyForGranularity(granularity)
	if wordTolerance <= 0 {
		wordTolerance = DefaultAdaptationWordTolerance
	}
	return strings.TrimSpace(fmt.Sprintf(`## 改编模式

granularity=%s
rewrite_policy=%s
word_tolerance=%.2f

## 用户目标

基于原书主线进行改编，暂无额外偏好输入。

## 主线保留规则

- 保持原书核心事件、人物命运和因果顺序不变。
- 每章写作前对照原文章节事实和 source refs。
- 角色关系、场景细节和节奏调整不得破坏原书主线动机。`,
		granularity, rewritePolicy, wordTolerance))
}

func normalizeAdaptationGranularity(value string) string {
	if strings.TrimSpace(value) == "" {
		return DefaultAdaptationGranularity
	}
	return domain.NormalizeAdaptationGranularity(value)
}

func normalizeAdaptationRewritePolicy(value string) string {
	if strings.TrimSpace(value) == "" {
		return DefaultAdaptationRewritePolicy
	}
	return domain.NormalizeAdaptationRewritePolicy(value)
}
