package tui

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/host"
)

func TestSimulationCommandsAreRegisteredAndNeedIdle(t *testing.T) {
	registry := commandRegistryInstance()
	for _, name := range []string{"simulate", "importsim"} {
		spec, ok := registry.Find(name)
		if !ok {
			t.Fatalf("expected /%s command to be registered", name)
		}
		if !spec.NeedsIdle {
			t.Fatalf("/%s should require idle state", name)
		}
	}

	items := builtinCommandItems()
	if !hasPaletteItem(items, "simulate") || !hasPaletteItem(items, "importsim") {
		t.Fatalf("expected simulate commands in palette: %+v", items)
	}
}

func TestSimulatePaletteItemAutoExecutes(t *testing.T) {
	items := builtinCommandItems()
	simulate, ok := findPaletteItem(items, "simulate")
	if !ok {
		t.Fatal("expected simulate palette item")
	}
	if !simulate.AutoExecute {
		t.Fatal("/simulate has no args and should execute when accepted from the command palette")
	}

	importsim, ok := findPaletteItem(items, "importsim")
	if !ok {
		t.Fatal("expected importsim palette item")
	}
	if importsim.AutoExecute {
		t.Fatal("/importsim requires a profile path and should not auto-execute")
	}
}

func TestSimulationCommandsAreBlockedWhileRunning(t *testing.T) {
	m := Model{snapshot: host.UISnapshot{IsRunning: true}, eventIndex: map[string]int{}}
	next, _ := m.handleSlashCommand(slashCommand{name: "simulate"})
	got := next.(Model)
	if len(got.events) != 1 || got.events[0].Category != "ERROR" {
		t.Fatalf("expected NeedsIdle to emit one error, got %+v", got.events)
	}
	if got.simulator != nil {
		t.Fatal("simulate modal should not start while runtime is running")
	}
}

func hasPaletteItem(items []commandPaletteItem, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

func findPaletteItem(items []commandPaletteItem, name string) (commandPaletteItem, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return commandPaletteItem{}, false
}
