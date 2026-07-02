package host

import (
	"context"
	"fmt"
	"time"

	"github.com/voocel/agentcore"
)

const addedModelProbeTimeout = 15 * time.Second

var addedModelConnectivityProbe = probeAddedModelConnectivity

func SetAddedModelConnectivityProbeForTest(probe func(context.Context, agentcore.ChatModel) error) func() {
	previous := addedModelConnectivityProbe
	addedModelConnectivityProbe = probe
	return func() {
		addedModelConnectivityProbe = previous
	}
}

func probeAddedModelConnectivity(ctx context.Context, model agentcore.ChatModel) error {
	if model == nil {
		return fmt.Errorf("模型连接测试失败: model is nil")
	}
	ctx, cancel := context.WithTimeout(ctx, addedModelProbeTimeout)
	defer cancel()

	_, err := model.Generate(ctx, []agentcore.Message{
		agentcore.UserMsg("Reply with OK only. This is a connection test."),
	}, nil, agentcore.WithMaxTokens(8))
	if err != nil {
		return fmt.Errorf("模型连接测试失败: %w", err)
	}
	return nil
}
