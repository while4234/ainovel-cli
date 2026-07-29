package host

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSimulationSummaryEnsuresReinforcedContractBeforeCoCreate(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)

	before, err := st.SimulationContracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if before != nil {
		t.Fatalf("contract unexpectedly exists before summary: %+v", before)
	}

	summary := buildSimulationProfileSummary(st, bootstrap.SimulationModeReinforced, 0)

	if summary == nil {
		t.Fatal("simulation summary is nil")
	}
	if summary.EffectiveMode != domain.SimulationModeReinforced {
		t.Fatalf("effective mode = %q, want reinforced", summary.EffectiveMode)
	}
	if summary.Contract == nil || !summary.Contract.Current {
		t.Fatalf("current contract missing from summary: %+v", summary.Contract)
	}
	if summary.Contract.Status != domain.SimulationContractActive {
		t.Fatalf("contract status = %q, want active", summary.Contract.Status)
	}

	persisted, err := st.SimulationContracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.RequestedMode != domain.SimulationModeReinforced {
		t.Fatalf("reinforced contract was not persisted: %+v", persisted)
	}
}
