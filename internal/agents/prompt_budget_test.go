package agents

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/promptcompile"
)

func TestBoundedAgentContextWindowEnforcesInputHardBudgets(t *testing.T) {
	for _, agent := range []promptcompile.Agent{
		promptcompile.AgentCoordinator,
		promptcompile.AgentWriter,
		promptcompile.AgentArchitect,
		promptcompile.AgentEditor,
	} {
		budget, ok := promptcompile.BudgetFor(agent)
		if !ok {
			t.Fatalf("missing budget for %s", agent)
		}
		window, reserve := boundedAgentContextWindow(128_000, agent)
		if inputCapacity := window - reserve; inputCapacity > budget.HardTokens {
			t.Fatalf("%s input capacity=%d exceeds hard=%d", agent, inputCapacity, budget.HardTokens)
		}
	}
}
