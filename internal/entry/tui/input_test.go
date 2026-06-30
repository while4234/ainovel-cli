package tui

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/host"
)

func TestRenderInputBoxClampsInputViewToOneLine(t *testing.T) {
	rendered := renderInputBox(
		"创作已完成\n创作已完成\n/export xfk_new.txt",
		"hint",
		host.UISnapshot{},
		"",
		120,
	)

	if got := strings.Count(rendered, "创作已完成"); got != 1 {
		t.Fatalf("input placeholder should render once, got %d:\n%s", got, rendered)
	}
	if strings.Contains(rendered, "/export xfk_new.txt") {
		t.Fatalf("input box should ignore lines after the first:\n%s", rendered)
	}
}
