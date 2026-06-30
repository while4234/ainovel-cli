package domain

import "strings"

const (
	AdaptationGranularityChapter = "chapter"
	AdaptationGranularityArc     = "arc"
	AdaptationGranularityFree    = "free"
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
	Brief             string                  `json:"brief"`
	MainlineRules     []string                `json:"mainline_rules,omitempty"`
	RelationshipGoals []string                `json:"relationship_goals,omitempty"`
	Chapters          []AdaptationChapterPlan `json:"chapters"`
}

// AdaptationChapterPlan defines one target chapter's source anchors and edits.
type AdaptationChapterPlan struct {
	Chapter         int      `json:"chapter"`
	Title           string   `json:"title"`
	SourceChapters  []int    `json:"source_chapters"`
	PreserveEvents  []string `json:"preserve_events,omitempty"`
	RequiredChanges []string `json:"required_changes,omitempty"`
	ForbiddenMoves  []string `json:"forbidden_moves,omitempty"`
}

// AdaptationCheck is saved after a draft has been checked against the plan.
type AdaptationCheck struct {
	Chapter     int      `json:"chapter"`
	DraftSHA256 string   `json:"draft_sha256"`
	Passed      bool     `json:"passed"`
	Summary     string   `json:"summary,omitempty"`
	Issues      []string `json:"issues,omitempty"`
	CheckedAt   string   `json:"checked_at"`
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
