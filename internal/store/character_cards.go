package store

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const characterCardLifecycleFile = "meta/character_cards/lifecycle.json"

type CharacterCardLifecycleConflictError struct {
	Expected int64
	Actual   int64
}

func (e *CharacterCardLifecycleConflictError) Error() string {
	return fmt.Sprintf("character card lifecycle revision conflict: expected %d, actual %d", e.Expected, e.Actual)
}

// CharacterCardStore persists lifecycle and source-mapping metadata only.
// Canonical character content remains owned by FoundationStore.
type CharacterCardStore struct {
	io *IO
	mu sync.Mutex
}

func newCharacterCardStore(io *IO) *CharacterCardStore {
	return &CharacterCardStore{io: io}
}

func (s *CharacterCardStore) Load(current domain.CharacterCardBinding) (*domain.CharacterCardLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.loadUnlocked()
	if err != nil || value == nil {
		return value, err
	}
	reconciled, err := domain.ReconcileCharacterCardLifecycle(*value, current)
	if err != nil {
		return nil, fmt.Errorf("reconcile character card lifecycle: %w", err)
	}
	return &reconciled, nil
}

func (s *CharacterCardStore) SaveCAS(
	candidate domain.CharacterCardLifecycle,
	expectedRevision int64,
	current domain.CharacterCardBinding,
) (domain.CharacterCardLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.loadUnlocked()
	if err != nil {
		return domain.CharacterCardLifecycle{}, err
	}
	actual := int64(0)
	if existing != nil {
		actual = existing.Revision
	}
	if actual != expectedRevision {
		return domain.CharacterCardLifecycle{}, &CharacterCardLifecycleConflictError{
			Expected: expectedRevision,
			Actual:   actual,
		}
	}
	candidate.Revision = actual
	normalized, err := domain.NormalizeCharacterCardLifecycle(candidate)
	if err != nil {
		return domain.CharacterCardLifecycle{}, fmt.Errorf("normalize character card lifecycle: %w", err)
	}
	normalized, err = domain.ReconcileCharacterCardLifecycle(normalized, current)
	if err != nil {
		return domain.CharacterCardLifecycle{}, fmt.Errorf("bind character card lifecycle: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if existing == nil {
		normalized.CreatedAt = now
	} else {
		normalized.CreatedAt = existing.CreatedAt
	}
	normalized.UpdatedAt = now
	if existing != nil && characterCardLifecycleEqual(*existing, normalized) {
		return *existing, nil
	}
	normalized.Revision = actual + 1
	if err := s.io.WriteJSON(characterCardLifecycleFile, normalized); err != nil {
		return domain.CharacterCardLifecycle{}, fmt.Errorf("save character card lifecycle: %w", err)
	}
	return normalized, nil
}

func (s *CharacterCardStore) loadUnlocked() (*domain.CharacterCardLifecycle, error) {
	var value domain.CharacterCardLifecycle
	if err := s.io.ReadJSON(characterCardLifecycleFile, &value); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load character card lifecycle: %w", err)
	}
	normalized, err := domain.NormalizeCharacterCardLifecycle(value)
	if err != nil {
		return nil, fmt.Errorf("normalize persisted character card lifecycle: %w", err)
	}
	normalized.Revision = value.Revision
	normalized.CreatedAt = value.CreatedAt
	normalized.UpdatedAt = value.UpdatedAt
	return &normalized, nil
}

func characterCardLifecycleEqual(left, right domain.CharacterCardLifecycle) bool {
	left.Revision, right.Revision = 0, 0
	left.UpdatedAt, right.UpdatedAt = "", ""
	return reflect.DeepEqual(left, right)
}
