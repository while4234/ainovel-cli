package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type SimulationStore struct{ io *IO }

func NewSimulationStore(io *IO) *SimulationStore { return &SimulationStore{io: io} }

const (
	simulationProfilePath         = "meta/simulation_profile.json"
	simulationMergeCheckpointPath = "meta/simulation_merge_checkpoint.json"
)

func (s *SimulationStore) Load() (*domain.SimulationProfile, error) {
	var profile domain.SimulationProfile
	if err := s.io.ReadJSON(simulationProfilePath, &profile); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := domain.ValidateSimulationProfile(&profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

func (s *SimulationStore) Save(profile domain.SimulationProfile) error {
	if profile.Version == "" {
		profile.Version = domain.SimulationProfileVersion
	}
	if err := domain.ValidateSimulationProfile(&profile); err != nil {
		return err
	}
	return s.io.WriteJSON(simulationProfilePath, profile)
}

func (s *SimulationStore) LoadMergeCheckpoint() (*domain.SimulationMergeCheckpoint, error) {
	var checkpoint domain.SimulationMergeCheckpoint
	if err := s.io.ReadJSON(simulationMergeCheckpointPath, &checkpoint); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := domain.ValidateSimulationMergeCheckpoint(&checkpoint); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

func (s *SimulationStore) SaveMergeCheckpoint(checkpoint domain.SimulationMergeCheckpoint) error {
	if checkpoint.Version == "" {
		checkpoint.Version = domain.SimulationMergeCheckpointVersion
	}
	if err := domain.ValidateSimulationMergeCheckpoint(&checkpoint); err != nil {
		return err
	}
	return s.io.WriteJSON(simulationMergeCheckpointPath, checkpoint)
}

func (s *SimulationStore) ClearMergeCheckpoint() error {
	return s.io.RemoveFile(simulationMergeCheckpointPath)
}
