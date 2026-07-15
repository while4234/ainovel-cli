package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestOriginalPlanningPassIsNotReusedAfterScopedContentChanges(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := originalAuditSignatureStructure()
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	audit := domain.OriginalPlanningAudit{Scope: "arc", Volume: 1, Arc: 1, FromChapter: 1, ToChapter: 1, Verdict: "pass", Summary: "current"}
	if err := domain.BindOriginalPlanningAudit(&audit, volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.OriginalPlanningAudits.Save(audit); err != nil {
		t.Fatal(err)
	}
	if current, err := st.OriginalPlanningAudits.Get("arc", 1, 1); err != nil || current == nil {
		t.Fatalf("current pass missing: current=%+v err=%v", current, err)
	}

	volumes[0].Arcs[0].Chapters[0].CoreEvent = "a materially different consequence"
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if stale, err := st.OriginalPlanningAudits.Get("arc", 1, 1); err != nil || stale != nil {
		t.Fatalf("stale pass was reused: stale=%+v err=%v", stale, err)
	}
	work, err := st.OriginalPlanningAudits.NextWork(st.Outline)
	if err != nil || work == nil || work.Kind != "audit_chapter" || work.FromChapter != 1 || work.ToChapter != 1 {
		t.Fatalf("next work after signature change = %+v err=%v", work, err)
	}
}

func TestSkeletonAuditProjectsDetailedChaptersOutOfItsSignature(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := originalAuditSignatureStructure()
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	audit := domain.OriginalPlanningAudit{Scope: "skeleton_volume", Volume: 1, Verdict: "pass", Summary: "skeleton current"}
	if err := st.OriginalPlanningAudits.Save(audit); err != nil {
		t.Fatal(err)
	}
	volumes[0].Arcs[0].Chapters[0].CoreEvent = "detail changed after skeleton approval"
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if current, err := st.OriginalPlanningAudits.Get("skeleton_volume", 1, 0); err != nil || current == nil {
		t.Fatalf("detail-only change invalidated skeleton projection: current=%+v err=%v", current, err)
	}
	volumes[0].Arcs[0].Goal = "a different causal phase contract"
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if stale, err := st.OriginalPlanningAudits.Get("skeleton_volume", 1, 0); err != nil || stale != nil {
		t.Fatalf("changed skeleton contract reused old pass: stale=%+v err=%v", stale, err)
	}
}

func TestOriginalPlanningRepairConsumesRejectedChapterAndRerunsAudit(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := originalAuditSignatureStructure()
	secondID := domain.LegacyStructureID("audit-test", domain.StructureKindChapter, "volume-1/arc-1/chapter-2")
	volumes[0].Arcs[0].Chapters = append(volumes[0].Arcs[0].Chapters, domain.OutlineEntry{
		ID: secondID, Chapter: 2, Title: "Refusal", CoreEvent: "the heroine refuses the demand", Hook: "the family escalates", Scenes: []string{"refuse the demand"},
	})
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	firstID := volumes[0].Arcs[0].Chapters[0].ID
	for _, audit := range []domain.OriginalPlanningAudit{
		{Scope: "chapter", ScopeID: firstID, Volume: 1, Arc: 1, FromChapter: 1, ToChapter: 1, Verdict: "pass", Summary: "chapter one passes"},
		{
			Scope: "chapter", ScopeID: secondID, Volume: 1, Arc: 1, FromChapter: 2, ToChapter: 2, Verdict: "revise", Summary: "chapter two conflicts",
			Issues: []domain.OriginalPlanningAuditIssue{{Severity: "major", Volume: 1, Arc: 1, FromChapter: 2, ToChapter: 2, Description: "conflicting refusal count", RepairInstruction: "make the count consistent"}},
		},
		{Scope: "arc", Volume: 1, Arc: 1, FromChapter: 1, ToChapter: 2, Verdict: "pass", Summary: "arc passes"},
		{Scope: "volume", Volume: 1, Verdict: "pass", Summary: "volume passes"},
		{Scope: "book_batch", FromVolume: 1, ToVolume: 1, Verdict: "pass", Summary: "batch passes"},
		{Scope: "book", Verdict: "pass", Summary: "book passes"},
	} {
		if err := st.OriginalPlanningAudits.Save(audit); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.OriginalPlanningAudits.InvalidateRepair(1, 1, 2, 2); err != nil {
		t.Fatal(err)
	}
	audits, err := st.OriginalPlanningAudits.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].Scope != "chapter" || audits[0].ScopeID != firstID || audits[0].Verdict != "pass" {
		t.Fatalf("audits after repair invalidation = %+v, want only chapter one pass", audits)
	}
	work, err := st.OriginalPlanningAudits.NextWork(st.Outline)
	if err != nil {
		t.Fatal(err)
	}
	if work == nil || work.Kind != "audit_chapter" || work.FromChapter != 2 || work.ToChapter != 2 {
		t.Fatalf("next work after repair = %+v, want chapter two re-audit", work)
	}
}

func originalAuditSignatureStructure() []domain.VolumeOutline {
	return []domain.VolumeOutline{{
		ID: domain.LegacyStructureID("audit-test", domain.StructureKindVolume, "volume-1"), Index: 1, Title: "Opening", Theme: "trust",
		Arcs: []domain.ArcOutline{{
			ID: domain.LegacyStructureID("audit-test", domain.StructureKindArc, "volume-1/arc-1"), Index: 1, Title: "Arrival", Goal: "form an alliance",
			Chapters: []domain.OutlineEntry{{
				ID: domain.LegacyStructureID("audit-test", domain.StructureKindChapter, "volume-1/arc-1/chapter-1"), Chapter: 1, Title: "Gate", CoreEvent: "the leads meet", Hook: "a warning", Scenes: []string{"the guarded gate"},
			}},
		}},
	}}
}
