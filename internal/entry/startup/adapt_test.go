package startup

import (
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
	if defaultPlan.AdaptRewritePolicy != domain.AdaptationRewriteFullRewrite {
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
	if explicitPlan.AdaptRewritePolicy != domain.AdaptationRewritePreserveDetails {
		t.Fatalf("explicit rewrite policy=%s", explicitPlan.AdaptRewritePolicy)
	}
	if explicitPlan.AdaptWordTolerance != 0.2 {
		t.Fatalf("explicit tolerance=%v", explicitPlan.AdaptWordTolerance)
	}
}
