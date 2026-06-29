package globalprompt

import (
	_ "embed"
	"strings"
)

//go:embed global-prompt.md
var embeddedGlobalPrompt string

// Text returns the embedded global prompt template after trimming surrounding
// whitespace. Replace global-prompt.md to customize the rule injected into
// every LLM system prompt.
func Text() string {
	return strings.TrimSpace(embeddedGlobalPrompt)
}

// Apply prepends the global prompt to a system prompt. It is intentionally
// idempotent so callers can use it at both resource-loading and LLM-call
// boundaries without duplicating the prefix.
func Apply(systemPrompt string) string {
	prefix := Text()
	if prefix == "" {
		return systemPrompt
	}

	body := strings.TrimSpace(systemPrompt)
	if body == "" {
		return prefix
	}
	if strings.HasPrefix(body, prefix) {
		return body
	}
	return prefix + "\n\n" + body
}
