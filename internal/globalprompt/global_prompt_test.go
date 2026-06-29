package globalprompt

import (
	"strings"
	"testing"
)

func TestApplyPrefixesSystemPrompt(t *testing.T) {
	prefix := Text()
	if prefix == "" {
		t.Fatal("global prompt template must not be empty")
	}

	got := Apply("role prompt")

	if !strings.HasPrefix(got, prefix+"\n\n") {
		t.Fatalf("global prompt was not prepended:\n%s", got)
	}
	if !strings.HasSuffix(got, "role prompt") {
		t.Fatalf("role prompt should remain after the prefix:\n%s", got)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	first := Apply("role prompt")
	second := Apply(first)

	if second != first {
		t.Fatalf("Apply should not duplicate the global prompt:\nfirst=%q\nsecond=%q", first, second)
	}
}
