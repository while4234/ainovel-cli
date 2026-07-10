package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
)

func TestAdaptationAuditReportRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeFree})
	if err := st.Adaptation.SaveAuditReport(report); err != nil {
		t.Fatalf("SaveAuditReport: %v", err)
	}
	loaded, err := st.Adaptation.LoadAuditReport()
	if err != nil {
		t.Fatalf("LoadAuditReport: %v", err)
	}
	if loaded == nil || loaded.Digest != report.Digest || !loaded.ReadOnly {
		t.Fatalf("loaded=%+v", loaded)
	}
}
