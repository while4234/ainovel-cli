package agents

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

type revisionFencedTool struct {
	inner agentcore.Tool
	store *store.RevisionStore
}

func revisionFenceWrites(revisions *store.RevisionStore, inner agentcore.Tool) agentcore.Tool {
	return &revisionFencedTool{inner: inner, store: revisions}
}

func (t *revisionFencedTool) Name() string           { return t.inner.Name() }
func (t *revisionFencedTool) Description() string    { return t.inner.Description() }
func (t *revisionFencedTool) Schema() map[string]any { return t.inner.Schema() }
func (t *revisionFencedTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	fence, ok := store.RevisionFenceFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("writable tool %q is missing its revision fence", t.inner.Name())
	}
	var result json.RawMessage
	err := t.store.WithFence(fence, func() error {
		var err error
		result, err = t.inner.Execute(ctx, args)
		return err
	})
	return result, err
}
