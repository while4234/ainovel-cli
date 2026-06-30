package assets

import (
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/globalprompt"
)

func TestLoadPromptsApplyGlobalPrompt(t *testing.T) {
	prompts := Load("").Prompts
	prefix := globalprompt.Text()
	value := reflect.ValueOf(prompts)
	typ := value.Type()

	for i := 0; i < value.NumField(); i++ {
		name := typ.Field(i).Name
		prompt := value.Field(i).String()
		if !strings.HasPrefix(prompt, prefix) {
			t.Fatalf("%s prompt does not start with the global prompt", name)
		}
	}
}

func TestLoadKeepsAdaptationGuidanceOutOfBaseWriterPrompt(t *testing.T) {
	bundle := Load("")
	if strings.Contains(bundle.Prompts.Writer, "某某内心独白") {
		t.Fatal("base writer prompt must not include adaptation-only inner-monologue label guidance")
	}
	if !strings.Contains(bundle.References.AdaptationWriter, "某某内心独白") {
		t.Fatal("adaptation writer guidance should carry the inner-monologue label rule")
	}
}
