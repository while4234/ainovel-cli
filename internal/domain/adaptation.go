package domain

import "strings"

const (
	AdaptationGranularityChapter = "chapter"
	AdaptationGranularityArc     = "arc"
	AdaptationGranularityFree    = "free"
)

const (
	AdaptationPlanStatusProposal  = "proposal"
	AdaptationPlanStatusConfirmed = "confirmed"
)

const (
	AdaptationRewriteFullRewrite     = "full_rewrite"
	AdaptationRewritePreserveDetails = "preserve_details"
)

// AdaptationSourceManifest records the imported source novel identity.
type AdaptationSourceManifest struct {
	SourcePath   string             `json:"source_path"`
	ChapterCount int                `json:"chapter_count"`
	Chapters     []AdaptationSource `json:"chapters"`
}

// AdaptationSource is one source chapter snapshot saved under meta/adaptation.
type AdaptationSource struct {
	Chapter int    `json:"chapter"`
	Title   string `json:"title"`
	SHA256  string `json:"sha256"`
	Path    string `json:"path"`
	Runes   int    `json:"runes"`
}

// AdaptationSourceReport stores source-chapter analysis for adaptation planning.
type AdaptationSourceReport struct {
	Chapter        int                 `json:"chapter"`
	Title          string              `json:"title"`
	SourceSHA256   string              `json:"source_sha256,omitempty"`
	Summary        string              `json:"summary"`
	Characters     []string            `json:"characters,omitempty"`
	CharacterFacts []string            `json:"character_facts,omitempty"`
	KeyEvents      []string            `json:"key_events,omitempty"`
	WorldRules     []string            `json:"world_rules,omitempty"`
	HookType       string              `json:"hook_type,omitempty"`
	DominantStrand string              `json:"dominant_strand,omitempty"`
	Timeline       []TimelineEvent     `json:"timeline,omitempty"`
	Foreshadow     []ForeshadowUpdate  `json:"foreshadow,omitempty"`
	Relationships  []RelationshipEntry `json:"relationships,omitempty"`
	StateChanges   []StateChange       `json:"state_changes,omitempty"`
}

// AdaptationSourceFoundation is the reusable foundation inferred from the source.
type AdaptationSourceFoundation struct {
	Premise    string          `json:"premise"`
	Characters []Character     `json:"characters"`
	WorldRules []WorldRule     `json:"world_rules"`
	Volumes    []VolumeOutline `json:"volumes"`
	Compass    *StoryCompass   `json:"compass,omitempty"`
}

// AdaptationPlan is the durable contract for rewriting the source as a new book.
type AdaptationPlan struct {
	Granularity       string                  `json:"granularity"`
	Status            string                  `json:"status"`
	RewritePolicy     string                  `json:"rewrite_policy"`
	Brief             string                  `json:"brief"`
	WordTolerance     float64                 `json:"word_tolerance,omitempty"`
	SourceTotalRunes  int                     `json:"source_total_runes,omitempty"`
	TargetTotalRunes  int                     `json:"target_total_runes,omitempty"`
	TargetMinRunes    int                     `json:"target_min_runes,omitempty"`
	TargetMaxRunes    int                     `json:"target_max_runes,omitempty"`
	MainlineRules     []string                `json:"mainline_rules,omitempty"`
	RelationshipGoals []string                `json:"relationship_goals,omitempty"`
	Chapters          []AdaptationChapterPlan `json:"chapters"`
}

// SourceRange records the inclusive source chapter coverage for one target
// chapter. It remains explicit even when SourceChapters has sparse anchors.
type SourceRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// AdaptationChapterPlan defines one target chapter's source anchors and edits.
type AdaptationChapterPlan struct {
	Chapter         int         `json:"chapter"`
	Title           string      `json:"title"`
	SourceChapters  []int       `json:"source_chapters"`
	SourceRunes     int         `json:"source_runes,omitempty"`
	TargetRunes     int         `json:"target_runes,omitempty"`
	TargetMinRunes  int         `json:"target_min_runes,omitempty"`
	TargetMaxRunes  int         `json:"target_max_runes,omitempty"`
	SourceRange     SourceRange `json:"source_range,omitempty"`
	IsAdded         bool        `json:"is_added,omitempty"`
	CoverageNote    string      `json:"coverage_note,omitempty"`
	PreserveEvents  []string    `json:"preserve_events,omitempty"`
	RequiredChanges []string    `json:"required_changes,omitempty"`
	ForbiddenMoves  []string    `json:"forbidden_moves,omitempty"`
}

// AdaptationCheck is saved after a draft has been checked against the plan.
type AdaptationCheck struct {
	Chapter        int                        `json:"chapter"`
	DraftSHA256    string                     `json:"draft_sha256"`
	Passed         bool                       `json:"passed"`
	Summary        string                     `json:"summary,omitempty"`
	Issues         []string                   `json:"issues,omitempty"`
	ChangeEvidence []AdaptationChangeEvidence `json:"change_evidence,omitempty"`
	CheckedAt      string                     `json:"checked_at"`
}

// AdaptationChangeEvidence records how a required adaptation change was
// integrated into prose instead of merely described as a patch note.
type AdaptationChangeEvidence struct {
	SourceChapter int    `json:"source_chapter,omitempty"`
	SourceAnchor  string `json:"source_anchor,omitempty"`
	Change        string `json:"change"`
	Integration   string `json:"integration,omitempty"`
}

// NormalizeAdaptationGranularity keeps the plan granularity constrained to the
// supported modes. Empty or unknown input falls back to chapter mode.
func NormalizeAdaptationGranularity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AdaptationGranularityArc:
		return AdaptationGranularityArc
	case AdaptationGranularityFree:
		return AdaptationGranularityFree
	default:
		return AdaptationGranularityChapter
	}
}

func StrictAdaptationGranularity(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case AdaptationGranularityChapter:
		return AdaptationGranularityChapter, true
	case AdaptationGranularityArc:
		return AdaptationGranularityArc, true
	case AdaptationGranularityFree:
		return AdaptationGranularityFree, true
	default:
		return "", false
	}
}

// NormalizeAdaptationRewritePolicy constrains rewrite policy. Empty and
// unknown values fall back to full rewrite for compatibility with old
// brief-only adaptation starts.
func NormalizeAdaptationRewritePolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AdaptationRewritePreserveDetails:
		return AdaptationRewritePreserveDetails
	default:
		return AdaptationRewriteFullRewrite
	}
}

// AdaptationRewritePolicyForGranularity is the canonical policy mapping for
// adaptation projects. preserve_details only works when target chapters map
// one-to-one with source chapters; broader restructuring must use full rewrite.
func AdaptationRewritePolicyForGranularity(granularity string) string {
	switch NormalizeAdaptationGranularity(granularity) {
	case AdaptationGranularityChapter:
		return AdaptationRewritePreserveDetails
	default:
		return AdaptationRewriteFullRewrite
	}
}

func NormalizeAdaptationPlanStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AdaptationPlanStatusProposal:
		return AdaptationPlanStatusProposal
	default:
		return AdaptationPlanStatusConfirmed
	}
}
