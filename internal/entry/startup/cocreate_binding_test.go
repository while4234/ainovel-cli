package startup

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/host"
)

func TestCoCreateDraftBindingUsesMonotonicRevisionAndNormalizedHash(t *testing.T) {
	session := NewCoCreateSession("seed")
	session.ApplyReply(host.CoCreateReply{Prompt: "draft  \r\nline", Raw: "assistant"})
	firstRevision, firstHash := session.DraftRevision(), session.DraftHash()
	if firstRevision <= 0 || firstHash == "" {
		t.Fatalf("first binding = revision %d hash %q", firstRevision, firstHash)
	}
	session.AppendUser("refine")
	if session.DraftRevision() != firstRevision || session.DraftHash() != firstHash {
		t.Fatal("user history length changed the semantic draft binding")
	}
	session.ApplyReply(host.CoCreateReply{Prompt: "draft\nline", Raw: "assistant"})
	if session.DraftRevision() <= firstRevision {
		t.Fatal("accepted draft revision did not advance monotonically")
	}
	if session.DraftHash() != firstHash {
		t.Fatal("line ending and trailing whitespace normalization changed the draft hash")
	}
	restored := NewCoCreateSessionFromSnapshot(session.Snapshot())
	if restored.DraftRevision() != session.DraftRevision() || restored.DraftHash() != session.DraftHash() {
		t.Fatalf("restored binding = %d/%q, want %d/%q", restored.DraftRevision(), restored.DraftHash(), session.DraftRevision(), session.DraftHash())
	}
}
