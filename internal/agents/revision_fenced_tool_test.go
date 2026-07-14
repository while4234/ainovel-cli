package agents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestRevisionFencedToolRejectsQueuedWriteAfterOwnershipRelease(t *testing.T) {
	revisions := store.NewRevisionStore(t.TempDir())
	lease, err := revisions.AcquireNormalFlow("queued-write-test")
	if err != nil {
		t.Fatal(err)
	}
	fence, err := revisions.FenceForNormalFlow(lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	inner := agentcore.NewFuncTool("write", "write", map[string]any{
		"type": "object",
	}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{"ok":true}`), nil
	})
	tool := revisionFenceWrites(revisions, inner)
	ctx := store.ContextWithRevisionFence(context.Background(), fence)
	if err := revisions.ReleaseNormalFlow(lease.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatal("queued write crossed a released ownership fence")
	}
	if called {
		t.Fatal("stale queued write reached the writable tool")
	}
}
