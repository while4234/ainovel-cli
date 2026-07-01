package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSteerReturnsPendingSteerPersistenceError(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "meta")); err != nil {
		t.Fatalf("remove meta dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write meta blocker: %v", err)
	}

	h := &Host{
		store:     st,
		events:    make(chan Event, 4),
		lifecycle: lifecycleIdle,
	}
	err := h.Steer("make the protagonist more cautious")
	if err == nil {
		t.Fatal("Steer returned nil error, want persistence failure")
	}
	if !strings.Contains(err.Error(), "set pending steer") {
		t.Fatalf("Steer error = %v, want pending steer context", err)
	}
}
