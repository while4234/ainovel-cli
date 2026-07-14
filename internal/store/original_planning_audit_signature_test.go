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
