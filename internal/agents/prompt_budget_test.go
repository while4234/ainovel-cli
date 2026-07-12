package agents

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/promptcompile"
)

func TestBoundedAgentContextWindowUsesRuntimeRoleWindows(t *testing.T) {
	tests := []struct {
		agent promptcompile.Agent
		want  int
	}{
		{agent: promptcompile.AgentCoordinator, want: 64_000},
		{agent: promptcompile.AgentWriter, want: 96_000},
		{agent: promptcompile.AgentArchitect, want: 96_000},
		{agent: promptcompile.AgentEditor, want: 128_000},
	}

	for _, tt := range tests {
		t.Run(string(tt.agent), func(t *testing.T) {
			window, reserve := boundedAgentContextWindow(200_000, tt.agent)
			if window != tt.want {
				t.Fatalf("window=%d, want %d", window, tt.want)
			}
			if reserve != bootstrap.CompactReserveTokens(tt.want) {
				t.Fatalf("reserve=%d, want %d", reserve, bootstrap.CompactReserveTokens(tt.want))
			}
		})
	}
}

func TestBoundedAgentContextWindowNeverExceedsModelWindow(t *testing.T) {
	window, reserve := boundedAgentContextWindow(32_000, promptcompile.AgentEditor)
	if window != 32_000 {
		t.Fatalf("window=%d, want model window 32000", window)
	}
	if reserve != bootstrap.CompactReserveTokens(32_000) {
		t.Fatalf("reserve=%d, want %d", reserve, bootstrap.CompactReserveTokens(32_000))
	}
}
