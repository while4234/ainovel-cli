package store

import (
	"errors"
	"testing"
)

func TestRequireManuscriptWriteReadyRetriesManuscriptPublicationRecovery(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	st.manuscriptPublicationRecoveryErr = errors.New("injected manuscript journal failure")
	st.recoveryErr = st.manuscriptPublicationRecoveryErr
	status := st.ManuscriptRecoveryState()
	if !status.Required || len(status.Owners) != 1 || status.Owners[0] != "manuscript_publication" {
		t.Fatalf("status = %+v", status)
	}
	if err := st.RequireManuscriptWriteReady(); err != nil {
		t.Fatalf("retry recovery: %v", err)
	}
	if status = st.ManuscriptRecoveryState(); status.Required {
		t.Fatalf("status after retry = %+v", status)
	}
}
