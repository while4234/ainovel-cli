// Package adapt implements source-novel adaptation preparation.
package adapt

import "time"

type Stage string

const (
	StageSplitting  Stage = "splitting"
	StageFoundation Stage = "foundation"
	StageChapter    Stage = "chapter"
	StagePlan       Stage = "plan"
	StageDone       Stage = "done"
	StageError      Stage = "error"
)

type Event struct {
	Time    time.Time
	Stage   Stage
	Current int
	Total   int
	Message string
	Err     error
}

type Options struct {
	SourcePath string
}

type ProposalOptions struct {
	Brief         string
	SourcePath    string
	Granularity   string
	RewritePolicy string
	WordTolerance float64
}

type Prompts struct {
	Foundation      string
	FoundationMerge string
	Analyzer        string
	Planner         string
}
