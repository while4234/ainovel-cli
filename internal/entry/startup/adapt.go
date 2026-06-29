package startup

import (
	"fmt"
	"strings"
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
	return Plan{
		Mode:        ModeAdaptNovel,
		DisplayName: "小说改编",
		RawPrompt:   brief,
	}, nil
}
