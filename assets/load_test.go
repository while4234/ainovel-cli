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
