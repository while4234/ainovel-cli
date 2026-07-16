package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
)

type modelExpansionRecommendationPlanner struct {
	model   agentcore.ChatModel
	prompts assets.Prompts
}

func (h *Host) ExpansionPlanner() *ExpansionPlanner {
	if h == nil || h.store == nil || h.models == nil {
		return nil
	}
	recommender := &modelExpansionRecommendationPlanner{model: h.models.ForStageWithFailover(bootstrap.StageSkeleton, nil), prompts: h.bundle.Prompts}
	return NewExpansionPlanner(h.store, recommender)
}

func (planner *modelExpansionRecommendationPlanner) RecommendExpansion(ctx context.Context, expansionContext ExpansionContext, request domain.ExpansionRequest) (domain.ExpansionRecommendation, error) {
	if planner == nil || planner.model == nil {
		return domain.ExpansionRecommendation{}, fmt.Errorf("expansion recommendation model is unavailable")
	}
	system := planner.prompts.NormalExpansionPlanner
	if expansionContext.Mode == domain.RevisionModeAdaptation {
		system = planner.prompts.AdaptationExpansionPlanner
	}
	payload, err := json.Marshal(struct {
		Context ExpansionContext        `json:"context"`
		Request domain.ExpansionRequest `json:"request"`
	}{expansionContext, request})
	if err != nil {
		return domain.ExpansionRecommendation{}, err
	}
	if len(system)+len(payload) > expansionContextBudgetBytes {
		return domain.ExpansionRecommendation{}, fmt.Errorf("compiled expansion request exceeds 60KiB")
	}
	response, err := planner.model.Generate(ctx, []agentcore.Message{agentcore.SystemMsg(system), agentcore.UserMsg(string(payload))}, nil, agentcore.WithMaxTokens(6000), agentcore.WithJSONMode())
	if err != nil {
		return domain.ExpansionRecommendation{}, err
	}
	if response == nil || strings.TrimSpace(response.Message.TextContent()) == "" {
		return domain.ExpansionRecommendation{}, fmt.Errorf("empty expansion recommendation")
	}
	text := strings.TrimSpace(response.Message.TextContent())
	if !strings.HasPrefix(text, "{") || !strings.HasSuffix(text, "}") {
		return domain.ExpansionRecommendation{}, fmt.Errorf("truncated expansion recommendation")
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	var recommendation domain.ExpansionRecommendation
	if err := decoder.Decode(&recommendation); err != nil {
		return recommendation, fmt.Errorf("decode expansion recommendation: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return recommendation, fmt.Errorf("expansion recommendation contains trailing JSON")
	}
	return recommendation, nil
}
