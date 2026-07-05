package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSimulationMergeCheckpointRoundTripAndClear(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}

	checkpoint := domain.SimulationMergeCheckpoint{
		Version:              domain.SimulationMergeCheckpointVersion,
		TotalReportCount:     1,
		ProcessedReportCount: 1,
		ProcessedBatchCount:  1,
		Reports: []domain.SimulationReportIdentity{{
			RelativePath: "a.txt",
			SHA256:       "sha-a",
		}},
		RollingSynthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"close narration"},
			},
		},
	}
	if err := st.Simulation.SaveMergeCheckpoint(checkpoint); err != nil {
		t.Fatalf("SaveMergeCheckpoint: %v", err)
	}

	loaded, err := st.Simulation.LoadMergeCheckpoint()
	if err != nil {
		t.Fatalf("LoadMergeCheckpoint: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected checkpoint")
	}
	if loaded.Reports[0].Fingerprint != domain.SimulationSourceFingerprint("a.txt", "sha-a") {
		t.Fatalf("fingerprint = %q", loaded.Reports[0].Fingerprint)
	}

	if err := st.Simulation.Save(domain.SimulationProfile{Version: domain.SimulationProfileVersion}); err != nil {
		t.Fatalf("Save profile: %v", err)
	}
	stillLoaded, err := st.Simulation.LoadMergeCheckpoint()
	if err != nil {
		t.Fatalf("LoadMergeCheckpoint after profile save: %v", err)
	}
	if stillLoaded == nil {
		t.Fatal("profile save should not clear independent checkpoint")
	}

	if err := st.Simulation.ClearMergeCheckpoint(); err != nil {
		t.Fatalf("ClearMergeCheckpoint: %v", err)
	}
	cleared, err := st.Simulation.LoadMergeCheckpoint()
	if err != nil {
		t.Fatalf("LoadMergeCheckpoint after clear: %v", err)
	}
	if cleared != nil {
		t.Fatalf("checkpoint after clear = %+v", cleared)
	}
}
