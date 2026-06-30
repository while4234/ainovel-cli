package startup

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

func TestPrepareAdaptNovelDefaultsAndExplicitOptions(t *testing.T) {
	defaultPlan, err := PrepareAdaptNovel(Request{
		UserPrompt: "改编 brief",
		NovelPath:  "source.txt",
	})
	if err != nil {
		t.Fatalf("PrepareAdaptNovel default: %v", err)
	}
	if defaultPlan.AdaptGranularity != domain.AdaptationGranularityChapter {
		t.Fatalf("default granularity=%s", defaultPlan.AdaptGranularity)
	}
	if defaultPlan.AdaptRewritePolicy != domain.AdaptationRewritePreserveDetails {
		t.Fatalf("default rewrite policy=%s", defaultPlan.AdaptRewritePolicy)
	}
	if defaultPlan.AdaptWordTolerance != adapt.DefaultWordTolerance {
		t.Fatalf("default tolerance=%v", defaultPlan.AdaptWordTolerance)
	}

	explicitPlan, err := PrepareAdaptNovel(Request{
		UserPrompt:         "改编 brief",
		NovelPath:          "source.txt",
		AdaptGranularity:   domain.AdaptationGranularityArc,
		AdaptRewritePolicy: domain.AdaptationRewritePreserveDetails,
		AdaptWordTolerance: 0.2,
	})
	if err != nil {
		t.Fatalf("PrepareAdaptNovel explicit: %v", err)
	}
	if explicitPlan.AdaptGranularity != domain.AdaptationGranularityArc {
		t.Fatalf("explicit granularity=%s", explicitPlan.AdaptGranularity)
	}
	if explicitPlan.AdaptRewritePolicy != domain.AdaptationRewriteFullRewrite {
		t.Fatalf("explicit rewrite policy=%s", explicitPlan.AdaptRewritePolicy)
	}
	if explicitPlan.AdaptWordTolerance != 0.2 {
		t.Fatalf("explicit tolerance=%v", explicitPlan.AdaptWordTolerance)
	}
}

func TestPrepareAdaptNovelDerivesRewritePolicyFromGranularity(t *testing.T) {
	cases := []struct {
		name        string
		granularity string
		wantPolicy  string
	}{
		{name: "chapter", granularity: domain.AdaptationGranularityChapter, wantPolicy: domain.AdaptationRewritePreserveDetails},
		{name: "arc", granularity: domain.AdaptationGranularityArc, wantPolicy: domain.AdaptationRewriteFullRewrite},
		{name: "free", granularity: domain.AdaptationGranularityFree, wantPolicy: domain.AdaptationRewriteFullRewrite},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PrepareAdaptNovel(Request{
				UserPrompt:         "改编 brief",
				NovelPath:          "source.txt",
				AdaptGranularity:   tc.granularity,
				AdaptRewritePolicy: domain.AdaptationRewritePreserveDetails,
			})
			if err != nil {
				t.Fatalf("PrepareAdaptNovel: %v", err)
			}
			if plan.AdaptRewritePolicy != tc.wantPolicy {
				t.Fatalf("rewrite policy=%s want %s", plan.AdaptRewritePolicy, tc.wantPolicy)
			}
		})
	}
}

func TestDefaultAdaptationBriefIncludesSelectedContract(t *testing.T) {
	brief := DefaultAdaptationBrief(domain.AdaptationGranularityArc, domain.AdaptationRewriteFullRewrite, 0.2)
	for _, want := range []string{
		"granularity=arc",
		"rewrite_policy=full_rewrite",
		"word_tolerance=0.20",
		"## 主线保留规则",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief missing %q:\n%s", want, brief)
		}
	}
}
