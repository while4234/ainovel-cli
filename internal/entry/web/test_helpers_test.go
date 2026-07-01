package web

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func testWebConfig(t *testing.T) bootstrap.Config {
	t.Helper()
	return bootstrap.Config{
		Provider:  "openai",
		ModelName: "gpt-test",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Type: "openai", APIKey: "sk-test"},
		},
	}
}
