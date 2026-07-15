package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const originalPlanningAuditsFile = "meta/original_planning/audits.json"

type OriginalPlanningAuditStore struct {
	io      *IO
	outline *OutlineStore
}

type OriginalPlanningWork struct {
	Kind        string
	Volume      int
	Arc         int
	FromVolume  int
	ToVolume    int
	FromChapter int
	ToChapter   int
	Audit       *domain.OriginalPlanningAudit
	Evidence    string
}

func NewOriginalPlanningAuditStore(io *IO, outlines ...*OutlineStore) *OriginalPlanningAuditStore {
	var outline *OutlineStore
	if len(outlines) > 0 {
		outline = outlines[0]
	}
	return &OriginalPlanningAuditStore{io: io, outline: outline}
}

func (s *OriginalPlanningAuditStore) Load() ([]domain.OriginalPlanningAudit, error) {
	var audits []domain.OriginalPlanningAudit
	if err := s.io.ReadJSON(originalPlanningAuditsFile, &audits); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return audits, nil
}

func (s *OriginalPlanningAuditStore) Save(audit domain.OriginalPlanningAudit) error {
	if audit.Verdict == "pass" && (audit.StructureSignature == "" || audit.ContentSignature == "") && s.outline != nil {
		volumes, err := s.outline.LoadLayeredOutline()
		if err != nil {
			return err
		}
		if err := domain.BindOriginalPlanningAudit(&audit, volumes); err != nil {
			return err
		}
	}
	return s.io.WithWriteLock(func() error {
		var audits []domain.OriginalPlanningAudit
		if err := s.io.ReadJSONUnlocked(originalPlanningAuditsFile, &audits); err != nil && !os.IsNotExist(err) {
			return err
		}
		attempt := 1
		kept := audits[:0]
		for _, existing := range audits {
			if sameOriginalPlanningAuditScope(existing, audit) {
				attempt = existing.Attempt + 1
				continue
			}
			kept = append(kept, existing)
		}
		audit.Attempt = attempt
		audit.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		kept = append(kept, audit)
		sort.SliceStable(kept, func(i, j int) bool {
			if kept[i].Volume != kept[j].Volume {
				return kept[i].Volume < kept[j].Volume
			}
			if kept[i].Arc != kept[j].Arc {
				return kept[i].Arc < kept[j].Arc
			}
			return originalPlanningAuditScopeRank(kept[i].Scope) < originalPlanningAuditScopeRank(kept[j].Scope)
		})
		return s.io.WriteJSONUnlocked(originalPlanningAuditsFile, kept)
	})
}

func (s *OriginalPlanningAuditStore) Get(scope string, volume, arc int) (*domain.OriginalPlanningAudit, error) {
	audits, err := s.loadCurrent()
	if err != nil {
		return nil, err
	}
	needle := domain.OriginalPlanningAudit{Scope: scope, Volume: volume, Arc: arc}
	for i := range audits {
		if sameOriginalPlanningAuditScope(audits[i], needle) {
			return &audits[i], nil
		}
	}
	return nil, nil
}

func (s *OriginalPlanningAuditStore) GetBookBatch(fromVolume, toVolume int) (*domain.OriginalPlanningAudit, error) {
	audits, err := s.loadCurrent()
	if err != nil {
		return nil, err
	}
	for i := range audits {
		if audits[i].Scope == "book_batch" && audits[i].FromVolume == fromVolume && audits[i].ToVolume == toVolume {
			return &audits[i], nil
		}
	}
	return nil, nil
}

func (s *OriginalPlanningAuditStore) Reset() error {
	return s.io.RemoveFile(originalPlanningAuditsFile)
}

// loadCurrent keeps revise instructions actionable while refusing to reuse a
// pass whose topology or scoped outline content no longer matches the project.
func (s *OriginalPlanningAuditStore) loadCurrent() ([]domain.OriginalPlanningAudit, error) {
	audits, err := s.Load()
	if err != nil || s.outline == nil {
		return audits, err
	}
	volumes, err := s.outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	current := make([]domain.OriginalPlanningAudit, 0, len(audits))
	for _, audit := range audits {
		if audit.Verdict != "pass" || domain.OriginalPlanningAuditCurrent(audit, volumes) {
			current = append(current, audit)
		}
	}
	return current, nil
}

// NextWork returns the next bounded generation/audit action. The order is
// deliberately serial: expand one <=4 chapter arc, audit it, finish and audit
// its volume, then synthesize at most two volumes per book batch.
func (s *OriginalPlanningAuditStore) NextWork(outline *OutlineStore) (*OriginalPlanningWork, error) {
	if outline == nil {
		return nil, fmt.Errorf("outline store is required")
	}
	volumes, err := outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return nil, err
	}
	audits, err := s.loadCurrent()
	if err != nil {
		return nil, err
	}
	// A failed gate always repairs its first precisely located issue before any
	// later material is generated or accepted.
	for i := len(audits) - 1; i >= 0; i-- {
		audit := audits[i]
		if audit.Verdict != "revise" || len(audit.Issues) == 0 {
			continue
		}
		issue := audit.Issues[0]
		copyAudit := audit
		return &OriginalPlanningWork{Kind: "repair_arc", Volume: issue.Volume, Arc: issue.Arc, FromChapter: issue.FromChapter, ToChapter: issue.ToChapter, Audit: &copyAudit}, nil
	}
	nextChapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			count := len(arc.Chapters)
			if count == 0 {
				count = arc.EstimatedChapters
			}
			from, to := nextChapter, nextChapter+count-1
			nextChapter += count
			if len(arc.Chapters) == 0 {
				return &OriginalPlanningWork{Kind: "expand_arc", Volume: volume.Index, Arc: arc.Index, FromChapter: from, ToChapter: to}, nil
			}
			for offset, chapter := range arc.Chapters {
				number := from + offset
				audit := findOriginalPlanningChapterAudit(audits, chapter.ID, number)
				if audit == nil || audit.Verdict != "pass" {
					return &OriginalPlanningWork{Kind: "audit_chapter", Volume: volume.Index, Arc: arc.Index, FromChapter: number, ToChapter: number}, nil
				}
			}
			audit := findOriginalPlanningAudit(audits, "arc", volume.Index, arc.Index, 0, 0)
			if audit == nil || audit.Verdict != "pass" {
				return &OriginalPlanningWork{Kind: "audit_arc", Volume: volume.Index, Arc: arc.Index, FromChapter: from, ToChapter: to}, nil
			}
		}
		volumeAudit := findOriginalPlanningAudit(audits, "volume", volume.Index, 0, 0, 0)
		if volumeAudit == nil || volumeAudit.Verdict != "pass" {
			return &OriginalPlanningWork{Kind: "audit_volume", Volume: volume.Index, Evidence: originalPlanningAuditEvidenceJSON(filterOriginalPlanningAudits(audits, "arc", volume.Index))}, nil
		}
	}
	for start := 0; start < len(volumes); start += 2 {
		end := min(start+1, len(volumes)-1)
		fromVolume, toVolume := volumes[start].Index, volumes[end].Index
		batch := findOriginalPlanningAudit(audits, "book_batch", 0, 0, fromVolume, toVolume)
		if batch == nil || batch.Verdict != "pass" {
			return &OriginalPlanningWork{Kind: "audit_book_batch", FromVolume: fromVolume, ToVolume: toVolume, Evidence: originalPlanningAuditEvidenceJSON(filterOriginalPlanningVolumeAudits(audits, fromVolume, toVolume))}, nil
		}
	}
	book := findOriginalPlanningAudit(audits, "book", 0, 0, 0, 0)
	if book == nil || book.Verdict != "pass" {
		return &OriginalPlanningWork{Kind: "audit_book", Evidence: originalPlanningAuditEvidenceJSON(filterOriginalPlanningAudits(audits, "book_batch", 0))}, nil
	}
	return &OriginalPlanningWork{Kind: "complete"}, nil
}

func findOriginalPlanningChapterAudit(audits []domain.OriginalPlanningAudit, chapterID string, chapter int) *domain.OriginalPlanningAudit {
	for index := range audits {
		audit := &audits[index]
		if audit.Scope == "chapter" && audit.FromChapter == chapter && audit.ToChapter == chapter &&
			strings.TrimSpace(audit.ScopeID) == strings.TrimSpace(chapterID) {
			return audit
		}
	}
	return nil
}

// NextSkeletonWork runs before the volume plan is exposed to the user. It
// audits one bounded volume at a time, then at most two volumes per synthesis,
// and finally the whole-book promise/ending contract from audit digests.
func (s *OriginalPlanningAuditStore) NextSkeletonWork(outline *OutlineStore) (*OriginalPlanningWork, error) {
	if outline == nil {
		return nil, fmt.Errorf("outline store is required")
	}
	volumes, err := outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return nil, err
	}
	audits, err := s.loadCurrent()
	if err != nil {
		return nil, err
	}
	for i := len(audits) - 1; i >= 0; i-- {
		audit := audits[i]
		if !isSkeletonAuditScope(audit.Scope) || audit.Verdict != "revise" || len(audit.Issues) == 0 {
			continue
		}
		issue := audit.Issues[0]
		copyAudit := audit
		return &OriginalPlanningWork{Kind: "repair_skeleton_volume", Volume: issue.Volume, Arc: issue.Arc, Audit: &copyAudit}, nil
	}
	for _, volume := range volumes {
		audit := findOriginalPlanningAudit(audits, "skeleton_volume", volume.Index, 0, 0, 0)
		if audit == nil || audit.Verdict != "pass" {
			return &OriginalPlanningWork{Kind: "audit_skeleton_volume", Volume: volume.Index}, nil
		}
	}
	for start := 0; start < len(volumes); start += 2 {
		end := min(start+1, len(volumes)-1)
		fromVolume, toVolume := volumes[start].Index, volumes[end].Index
		batch := findOriginalPlanningAudit(audits, "skeleton_book_batch", 0, 0, fromVolume, toVolume)
		if batch == nil || batch.Verdict != "pass" {
			return &OriginalPlanningWork{
				Kind: "audit_skeleton_book_batch", FromVolume: fromVolume, ToVolume: toVolume,
				Evidence: originalPlanningAuditEvidenceJSON(filterOriginalPlanningAudits(audits, "skeleton_volume", 0)),
			}, nil
		}
	}
	book := findOriginalPlanningAudit(audits, "skeleton_book", 0, 0, 0, 0)
	if book == nil || book.Verdict != "pass" {
		return &OriginalPlanningWork{
			Kind:     "audit_skeleton_book",
			Evidence: originalPlanningAuditEvidenceJSON(filterOriginalPlanningAudits(audits, "skeleton_book_batch", 0)),
		}, nil
	}
	return &OriginalPlanningWork{Kind: "skeleton_complete"}, nil
}

func isSkeletonAuditScope(scope string) bool {
	return scope == "skeleton_volume" || scope == "skeleton_book_batch" || scope == "skeleton_book"
}

func filterOriginalPlanningAudits(audits []domain.OriginalPlanningAudit, scope string, volume int) []domain.OriginalPlanningAudit {
	var out []domain.OriginalPlanningAudit
	for _, audit := range audits {
		if audit.Scope == scope && (volume == 0 || audit.Volume == volume) && audit.Verdict == "pass" {
			out = append(out, audit)
		}
	}
	return out
}

func filterOriginalPlanningVolumeAudits(audits []domain.OriginalPlanningAudit, fromVolume, toVolume int) []domain.OriginalPlanningAudit {
	var out []domain.OriginalPlanningAudit
	for _, audit := range audits {
		if audit.Scope == "volume" && audit.Volume >= fromVolume && audit.Volume <= toVolume && audit.Verdict == "pass" {
			out = append(out, audit)
		}
	}
	return out
}

func originalPlanningAuditEvidenceJSON(audits []domain.OriginalPlanningAudit) string {
	data, _ := json.Marshal(audits)
	return string(data)
}

func findOriginalPlanningAudit(audits []domain.OriginalPlanningAudit, scope string, volume, arc, fromVolume, toVolume int) *domain.OriginalPlanningAudit {
	for i := range audits {
		audit := &audits[i]
		if audit.Scope == scope && audit.Volume == volume && audit.Arc == arc && audit.FromVolume == fromVolume && audit.ToVolume == toVolume {
			return audit
		}
	}
	return nil
}

// InvalidateRepair consumes chapter failures in the repaired window and
// removes the repaired arc plus enclosing volume/book audits. Earlier chapter
// passes outside the repair window remain valid; every enclosing gate reruns
// against the repaired causal chain.
func (s *OriginalPlanningAuditStore) InvalidateRepair(volume, arc, fromChapter, toChapter int) error {
	if volume <= 0 || arc <= 0 {
		return fmt.Errorf("repair audit invalidation requires volume and arc")
	}
	if (fromChapter == 0) != (toChapter == 0) || fromChapter < 0 || toChapter < fromChapter {
		return fmt.Errorf("repair audit invalidation requires a valid chapter window or 0-0")
	}
	return s.io.WithWriteLock(func() error {
		var audits []domain.OriginalPlanningAudit
		if err := s.io.ReadJSONUnlocked(originalPlanningAuditsFile, &audits); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		kept := audits[:0]
		for _, audit := range audits {
			removeChapter := audit.Scope == "chapter" && ((fromChapter > 0 && audit.FromChapter <= toChapter && audit.ToChapter >= fromChapter) ||
				(fromChapter == 0 && audit.Volume == volume && audit.Arc == arc))
			remove := removeChapter || audit.Scope == "book" ||
				(audit.Scope == "book_batch" && audit.FromVolume <= volume && audit.ToVolume >= volume) ||
				(audit.Scope == "volume" && audit.Volume == volume) ||
				(audit.Scope == "arc" && audit.Volume == volume && audit.Arc == arc)
			if !remove {
				kept = append(kept, audit)
			}
		}
		if len(kept) == 0 {
			return s.io.RemoveFileUnlocked(originalPlanningAuditsFile)
		}
		return s.io.WriteJSONUnlocked(originalPlanningAuditsFile, kept)
	})
}

// InvalidateSkeletonRepair reruns the changed volume, adjacent handoff
// volumes, and every synthesis that could depend on the replaced volume.
func (s *OriginalPlanningAuditStore) InvalidateSkeletonRepair(volume int) error {
	if volume <= 0 {
		return fmt.Errorf("skeleton repair audit invalidation requires volume")
	}
	return s.io.WithWriteLock(func() error {
		var audits []domain.OriginalPlanningAudit
		if err := s.io.ReadJSONUnlocked(originalPlanningAuditsFile, &audits); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		kept := audits[:0]
		for _, audit := range audits {
			remove := audit.Scope == "skeleton_book" ||
				(audit.Scope == "skeleton_book_batch" && audit.ToVolume >= volume-1 && audit.FromVolume <= volume+1) ||
				(audit.Scope == "skeleton_volume" && audit.Volume >= volume-1 && audit.Volume <= volume+1)
			if !remove {
				kept = append(kept, audit)
			}
		}
		if len(kept) == 0 {
			return s.io.RemoveFileUnlocked(originalPlanningAuditsFile)
		}
		return s.io.WriteJSONUnlocked(originalPlanningAuditsFile, kept)
	})
}

func sameOriginalPlanningAuditScope(a, b domain.OriginalPlanningAudit) bool {
	if a.Scope != b.Scope || a.Volume != b.Volume || a.Arc != b.Arc ||
		a.FromVolume != b.FromVolume || a.ToVolume != b.ToVolume {
		return false
	}
	if a.Scope == "chapter" {
		return a.ScopeID == b.ScopeID && a.FromChapter == b.FromChapter && a.ToChapter == b.ToChapter
	}
	return true
}

func originalPlanningAuditScopeRank(scope string) int {
	switch scope {
	case "skeleton_volume":
		return 0
	case "skeleton_book_batch":
		return 1
	case "skeleton_book":
		return 2
	case "arc":
		return 3
	case "volume":
		return 4
	case "book_batch":
		return 5
	case "book":
		return 6
	default:
		return 5
	}
}
