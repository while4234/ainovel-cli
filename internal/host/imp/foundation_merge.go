package imp

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	sourceOutlineArcSize          = 10
	DefaultFoundationMergeRunes   = 70000
	foundationPartialPremiseRunes = 2200
	foundationPartialFactRunes    = 320
)

type FoundationMergeBatchEvent struct {
	Index int
	Total int
	From  int
	To    int
	Final bool
}

func MergeFoundationFromReports(
	ctx context.Context,
	llm LLMChat,
	systemPrompt string,
	reports []domain.AdaptationSourceReport,
	opts StructuredCallOptions,
) (*FoundationResult, error) {
	if llm == nil {
		return nil, fmt.Errorf("llm is nil")
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("no source reports to merge")
	}

	system := cleanLLMText(strings.ReplaceAll(systemPrompt, "${chapter_count}", fmt.Sprintf("%d", len(reports))))
	user := cleanLLMText(buildFoundationMergeUserPrompt(reports))
	result, err := runStructuredCall(ctx, llm, []agentcore.Message{
		agentcore.SystemMsg(system),
		agentcore.UserMsg(user),
	}, parseFoundationMergeOutput, opts)
	if err != nil {
		return nil, err
	}
	result.Volumes = BuildSourceOutlineFromReports(reports)
	if got := len(domain.FlattenOutline(result.Volumes)); got != len(reports) {
		return nil, fmt.Errorf("generated source outline chapter count mismatch: got %d, want %d", got, len(reports))
	}
	if result.Compass != nil && result.Compass.LastUpdated == 0 {
		result.Compass.LastUpdated = len(reports)
	}
	return result, nil
}

func MergeFoundationFromReportsBatched(
	ctx context.Context,
	llm LLMChat,
	systemPrompt string,
	reports []domain.AdaptationSourceReport,
	opts StructuredCallOptions,
	batchRuneLimit int,
	onBatch func(FoundationMergeBatchEvent),
) (*FoundationResult, error) {
	if llm == nil {
		return nil, fmt.Errorf("llm is nil")
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("no source reports to merge")
	}
	if batchRuneLimit <= 0 {
		batchRuneLimit = DefaultFoundationMergeRunes
	}

	batches := FoundationMergeReportBatches(reports, batchRuneLimit)
	if len(batches) <= 1 {
		if onBatch != nil {
			onBatch(FoundationMergeBatchEvent{
				Index: 1,
				Total: 1,
				From:  reports[0].Chapter,
				To:    reports[len(reports)-1].Chapter,
			})
		}
		return MergeFoundationFromReports(ctx, llm, systemPrompt, reports, opts)
	}

	partials := make([]FoundationMergePartial, 0, len(batches))
	for i, batch := range batches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if onBatch != nil {
			onBatch(FoundationMergeBatchEvent{
				Index: i + 1,
				Total: len(batches),
				From:  batch[0].Chapter,
				To:    batch[len(batch)-1].Chapter,
			})
		}
		result, err := MergeFoundationFromReports(ctx, llm, systemPrompt, batch, opts)
		if err != nil {
			return nil, fmt.Errorf("merge source foundation batch %d/%d (chapters %d-%d): %w",
				i+1, len(batches), batch[0].Chapter, batch[len(batch)-1].Chapter, err)
		}
		partials = append(partials, FoundationMergePartial{
			Index:  i + 1,
			From:   batch[0].Chapter,
			To:     batch[len(batch)-1].Chapter,
			Result: result,
		})
	}

	if onBatch != nil {
		onBatch(FoundationMergeBatchEvent{
			Index: len(batches) + 1,
			Total: len(batches) + 1,
			From:  reports[0].Chapter,
			To:    reports[len(reports)-1].Chapter,
			Final: true,
		})
	}
	result, err := MergeFoundationPartialsBatched(ctx, llm, systemPrompt, partials, len(reports), opts, batchRuneLimit, onBatch)
	if err != nil {
		return nil, err
	}
	result.Volumes = BuildSourceOutlineFromReports(reports)
	if got := len(domain.FlattenOutline(result.Volumes)); got != len(reports) {
		return nil, fmt.Errorf("generated source outline chapter count mismatch: got %d, want %d", got, len(reports))
	}
	if result.Compass != nil && result.Compass.LastUpdated == 0 {
		result.Compass.LastUpdated = len(reports)
	}
	return result, nil
}

type FoundationMergePartial struct {
	Index          int
	From           int
	To             int
	InputSignature string
	Result         *FoundationResult
}

func FoundationMergeReportBatches(reports []domain.AdaptationSourceReport, runeLimit int) [][]domain.AdaptationSourceReport {
	if runeLimit <= 0 {
		runeLimit = DefaultFoundationMergeRunes
	}
	var batches [][]domain.AdaptationSourceReport
	var current []domain.AdaptationSourceReport
	currentRunes := 0
	for _, report := range reports {
		reportRunes := foundationMergeReportRunes(report)
		if len(current) > 0 && currentRunes+reportRunes > runeLimit {
			batches = append(batches, current)
			current = nil
			currentRunes = 0
		}
		current = append(current, report)
		currentRunes += reportRunes
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func foundationMergeReportRunes(report domain.AdaptationSourceReport) int {
	var sb strings.Builder
	writeFoundationMergeReport(&sb, report)
	return utf8.RuneCountInString(sb.String())
}

func MergeFoundationPartialsBatched(
	ctx context.Context,
	llm LLMChat,
	systemPrompt string,
	partials []FoundationMergePartial,
	totalReports int,
	opts StructuredCallOptions,
	batchRuneLimit int,
	onBatch func(FoundationMergeBatchEvent),
) (*FoundationResult, error) {
	if len(partials) == 0 {
		return nil, fmt.Errorf("no source foundation batches to merge")
	}
	if len(partials) == 1 {
		return partials[0].Result, nil
	}
	if batchRuneLimit <= 0 {
		batchRuneLimit = DefaultFoundationMergeRunes
	}

	level := 1
	current := partials
	for len(current) > 1 {
		groups := FoundationMergePartialBatches(current, batchRuneLimit)
		if len(groups) == 1 {
			result, err := MergeFoundationPartials(ctx, llm, systemPrompt, groups[0], totalReports, opts)
			if err != nil {
				return nil, err
			}
			return result, nil
		}
		next := make([]FoundationMergePartial, 0, len(groups))
		for i, group := range groups {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if onBatch != nil {
				onBatch(FoundationMergeBatchEvent{
					Index: i + 1,
					Total: len(groups),
					From:  group[0].From,
					To:    group[len(group)-1].To,
					Final: true,
				})
			}
			result, err := MergeFoundationPartials(ctx, llm, systemPrompt, group, totalReports, opts)
			if err != nil {
				return nil, fmt.Errorf("merge source foundation summary level %d batch %d/%d (chapters %d-%d): %w",
					level, i+1, len(groups), group[0].From, group[len(group)-1].To, err)
			}
			next = append(next, FoundationMergePartial{
				Index:  i + 1,
				From:   group[0].From,
				To:     group[len(group)-1].To,
				Result: result,
			})
		}
		current = next
		level++
	}
	return current[0].Result, nil
}

func FoundationMergePartialBatches(partials []FoundationMergePartial, runeLimit int) [][]FoundationMergePartial {
	if runeLimit <= 0 {
		runeLimit = DefaultFoundationMergeRunes
	}
	var batches [][]FoundationMergePartial
	var current []FoundationMergePartial
	currentRunes := 0
	for _, partial := range partials {
		partialRunes := foundationMergePartialRunes(partial)
		if len(current) > 0 && currentRunes+partialRunes > runeLimit {
			batches = append(batches, current)
			current = nil
			currentRunes = 0
		}
		current = append(current, partial)
		currentRunes += partialRunes
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func foundationMergePartialRunes(partial FoundationMergePartial) int {
	if partial.Result == nil {
		return 0
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Partial %d: source chapters %d-%d\n", partial.Index, partial.From, partial.To)
	writeMergeFact(&sb, "Premise", compactFact(partial.Result.Premise, foundationPartialPremiseRunes))
	writePartialCharacters(&sb, partial.Result.Characters)
	writePartialWorldRules(&sb, partial.Result.WorldRules)
	writePartialCompass(&sb, partial.Result.Compass)
	return utf8.RuneCountInString(sb.String())
}

func MergeFoundationPartials(
	ctx context.Context,
	llm LLMChat,
	systemPrompt string,
	partials []FoundationMergePartial,
	totalReports int,
	opts StructuredCallOptions,
) (*FoundationResult, error) {
	if len(partials) == 0 {
		return nil, fmt.Errorf("no source foundation batches to merge")
	}
	system := cleanLLMText(strings.ReplaceAll(systemPrompt, "${chapter_count}", fmt.Sprintf("%d", totalReports)))
	user := cleanLLMText(buildFoundationPartialMergeUserPrompt(partials, totalReports))
	result, err := runStructuredCall(ctx, llm, []agentcore.Message{
		agentcore.SystemMsg(system),
		agentcore.UserMsg(user),
	}, parseFoundationMergeOutput, opts)
	if err != nil {
		return nil, fmt.Errorf("merge source foundation batch summaries: %w", err)
	}
	return result, nil
}

func buildFoundationMergeUserPrompt(reports []domain.AdaptationSourceReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The following %d items are compact source-chapter fact reports. They are not original prose.\n", len(reports))
	sb.WriteString("Merge only these facts into a foundation. Preserve causal order and uncertainty.\n\n")
	for _, report := range reports {
		writeFoundationMergeReport(&sb, report)
	}
	return sb.String()
}

func writeFoundationMergeReport(sb *strings.Builder, report domain.AdaptationSourceReport) {
	title := strings.TrimSpace(report.Title)
	if title == "" {
		title = fmt.Sprintf("Chapter %d", report.Chapter)
	}
	fmt.Fprintf(sb, "## Chapter %d: %s\n", report.Chapter, cleanLLMText(title))
	writeMergeFact(sb, "Summary", report.Summary)
	writeMergeList(sb, "Appearing characters", report.Characters)
	writeMergeList(sb, "Character facts", report.CharacterFacts)
	writeMergeList(sb, "Key events", report.KeyEvents)
	writeMergeList(sb, "World rules", report.WorldRules)
	writeMergeFact(sb, "Hook type", report.HookType)
	writeMergeFact(sb, "Dominant strand", report.DominantStrand)
	writeTimelineFacts(sb, report.Timeline)
	writeForeshadowFacts(sb, report.Foreshadow)
	writeRelationshipFacts(sb, report.Relationships)
	writeStateChangeFacts(sb, report.StateChanges)
	sb.WriteString("\n")
}

func buildFoundationPartialMergeUserPrompt(partials []FoundationMergePartial, totalReports int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The source novel has %d chapter reports. They were merged into %d consecutive partial foundations to keep each request small.\n", totalReports, len(partials))
	sb.WriteString("Merge these partial foundations into one all-book foundation. Preserve source causal order and keep only facts supported by the partial foundations.\n")
	sb.WriteString("Return the same required === PREMISE ===, === CHARACTERS ===, === WORLD_RULES ===, and === COMPASS === sections.\n\n")
	for _, partial := range partials {
		result := partial.Result
		if result == nil {
			continue
		}
		fmt.Fprintf(&sb, "## Partial %d: source chapters %d-%d\n", partial.Index, partial.From, partial.To)
		writeMergeFact(&sb, "Premise", compactFact(result.Premise, foundationPartialPremiseRunes))
		writePartialCharacters(&sb, result.Characters)
		writePartialWorldRules(&sb, result.WorldRules)
		writePartialCompass(&sb, result.Compass)
		sb.WriteString("\n")
	}
	return sb.String()
}

func writePartialCharacters(sb *strings.Builder, characters []domain.Character) {
	if len(characters) == 0 {
		return
	}
	fmt.Fprintln(sb, "- Characters:")
	for _, character := range characters {
		name := compactFact(character.Name, 80)
		if name == "" {
			continue
		}
		role := compactFact(character.Role, 80)
		desc := compactFact(firstNonEmpty([]string{character.Description, character.Arc}, ""), foundationPartialFactRunes)
		if role != "" || desc != "" {
			fmt.Fprintf(sb, "  - %s (%s): %s\n", name, role, desc)
			continue
		}
		fmt.Fprintf(sb, "  - %s\n", name)
	}
}

func writePartialWorldRules(sb *strings.Builder, rules []domain.WorldRule) {
	if len(rules) == 0 {
		return
	}
	fmt.Fprintln(sb, "- World rules:")
	for _, rule := range rules {
		line := compactFact(rule.Rule, foundationPartialFactRunes)
		if line == "" {
			continue
		}
		if strings.TrimSpace(rule.Category) != "" {
			line = compactFact(rule.Category, 80) + ": " + line
		}
		if strings.TrimSpace(rule.Boundary) != "" {
			line += " / boundary: " + compactFact(rule.Boundary, 180)
		}
		fmt.Fprintf(sb, "  - %s\n", line)
	}
}

func writePartialCompass(sb *strings.Builder, compass *domain.StoryCompass) {
	if compass == nil {
		return
	}
	fmt.Fprintln(sb, "- Compass:")
	writeMergeFact(sb, "Ending direction", compass.EndingDirection)
	writeMergeList(sb, "Open threads", compass.OpenThreads)
	writeMergeFact(sb, "Estimated scale", compass.EstimatedScale)
}

func writeMergeFact(sb *strings.Builder, label, value string) {
	value = compactFact(value, 800)
	if value == "" {
		return
	}
	fmt.Fprintf(sb, "- %s: %s\n", label, value)
}

func writeMergeList(sb *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(sb, "- %s:\n", label)
	for _, value := range values {
		value = compactFact(value, 500)
		if value != "" {
			fmt.Fprintf(sb, "  - %s\n", value)
		}
	}
}

func writeTimelineFacts(sb *strings.Builder, events []domain.TimelineEvent) {
	if len(events) == 0 {
		return
	}
	fmt.Fprintln(sb, "- Timeline:")
	for _, event := range events {
		line := compactFact(event.Event, 500)
		if line == "" {
			continue
		}
		if strings.TrimSpace(event.Time) != "" {
			line = compactFact(event.Time, 120) + ": " + line
		}
		if len(event.Characters) > 0 {
			line += " (" + strings.Join(event.Characters, ", ") + ")"
		}
		fmt.Fprintf(sb, "  - %s\n", line)
	}
}

func writeForeshadowFacts(sb *strings.Builder, updates []domain.ForeshadowUpdate) {
	if len(updates) == 0 {
		return
	}
	fmt.Fprintln(sb, "- Foreshadow:")
	for _, update := range updates {
		line := compactFact(update.Action+" "+update.ID, 160)
		if desc := compactFact(update.Description, 400); desc != "" {
			line += ": " + desc
		}
		if strings.TrimSpace(line) != "" {
			fmt.Fprintf(sb, "  - %s\n", line)
		}
	}
}

func writeRelationshipFacts(sb *strings.Builder, relations []domain.RelationshipEntry) {
	if len(relations) == 0 {
		return
	}
	fmt.Fprintln(sb, "- Relationships:")
	for _, relation := range relations {
		line := strings.TrimSpace(relation.CharacterA + " / " + relation.CharacterB)
		if desc := compactFact(relation.Relation, 400); desc != "" {
			line += ": " + desc
		}
		if strings.TrimSpace(line) != "/" {
			fmt.Fprintf(sb, "  - %s\n", line)
		}
	}
}

func writeStateChangeFacts(sb *strings.Builder, changes []domain.StateChange) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintln(sb, "- State changes:")
	for _, change := range changes {
		entity := compactFact(change.Entity, 120)
		field := compactFact(change.Field, 80)
		next := compactFact(change.NewValue, 260)
		if entity == "" || field == "" || next == "" {
			continue
		}
		line := fmt.Sprintf("%s.%s -> %s", entity, field, next)
		if reason := compactFact(change.Reason, 300); reason != "" {
			line += " because " + reason
		}
		fmt.Fprintf(sb, "  - %s\n", line)
	}
}

func parseFoundationMergeOutput(text string) (*FoundationResult, error) {
	text = cleanLLMText(text)
	env := parseTaggedEnvelope(text)
	if env == nil {
		return nil, fmt.Errorf("no === TAG === envelope found in foundation merge output")
	}
	if err := requireTags(env, "PREMISE", "CHARACTERS", "WORLD_RULES", "COMPASS"); err != nil {
		return nil, err
	}

	premise := stripFences(env["PREMISE"])
	if !strings.HasPrefix(strings.TrimLeft(premise, " \t\n"), "#") {
		return nil, fmt.Errorf("premise must start with a Markdown heading line")
	}

	var characters []domain.Character
	if err := decodeJSON("characters", env["CHARACTERS"], &characters); err != nil {
		return nil, err
	}
	if len(characters) == 0 {
		return nil, fmt.Errorf("characters array is empty")
	}

	var worldRules []domain.WorldRule
	if err := decodeJSON("world_rules", env["WORLD_RULES"], &worldRules); err != nil {
		return nil, err
	}

	var compass domain.StoryCompass
	if err := decodeJSON("compass", env["COMPASS"], &compass); err != nil {
		return nil, err
	}

	return &FoundationResult{
		Premise:    premise,
		Characters: characters,
		WorldRules: worldRules,
		Compass:    &compass,
	}, nil
}

func BuildSourceOutlineFromReports(reports []domain.AdaptationSourceReport) []domain.VolumeOutline {
	arcs := make([]domain.ArcOutline, 0, (len(reports)+sourceOutlineArcSize-1)/sourceOutlineArcSize)
	for start := 0; start < len(reports); start += sourceOutlineArcSize {
		end := start + sourceOutlineArcSize
		if end > len(reports) {
			end = len(reports)
		}
		arcReports := reports[start:end]
		arc := domain.ArcOutline{
			Index:             len(arcs) + 1,
			Title:             fmt.Sprintf("Source Chapters %d-%d", arcReports[0].Chapter, arcReports[len(arcReports)-1].Chapter),
			Goal:              outlineGoal(arcReports),
			EstimatedChapters: len(arcReports),
			Chapters:          make([]domain.OutlineEntry, 0, len(arcReports)),
		}
		for _, report := range arcReports {
			arc.Chapters = append(arc.Chapters, outlineEntryFromReport(report))
		}
		arcs = append(arcs, arc)
	}
	return []domain.VolumeOutline{{
		Index: 1,
		Title: "Source Novel",
		Theme: "Preserve the source causal chain and chapter anchors.",
		Arcs:  arcs,
	}}
}

func outlineEntryFromReport(report domain.AdaptationSourceReport) domain.OutlineEntry {
	title := strings.TrimSpace(report.Title)
	if title == "" {
		title = fmt.Sprintf("Chapter %d", report.Chapter)
	}
	scenes := compactList(report.KeyEvents, 5, 500)
	if len(scenes) == 0 && strings.TrimSpace(report.Summary) != "" {
		scenes = []string{compactFact(report.Summary, 500)}
	}
	return domain.OutlineEntry{
		Chapter:   report.Chapter,
		Title:     title,
		CoreEvent: firstNonEmpty(compactList(report.KeyEvents, 1, 500), compactFact(report.Summary, 500)),
		Hook:      outlineHook(report),
		Scenes:    scenes,
	}
}

func outlineGoal(reports []domain.AdaptationSourceReport) string {
	for _, report := range reports {
		if events := compactList(report.KeyEvents, 1, 500); len(events) > 0 {
			return events[0]
		}
		if summary := compactFact(report.Summary, 500); summary != "" {
			return summary
		}
	}
	return "Track the source chapters in order."
}

func outlineHook(report domain.AdaptationSourceReport) string {
	events := compactList(report.KeyEvents, len(report.KeyEvents), 500)
	if len(events) > 0 {
		hookType := compactFact(report.HookType, 80)
		if hookType != "" {
			return hookType + ": " + events[len(events)-1]
		}
		return events[len(events)-1]
	}
	return compactFact(report.HookType, 200)
}

func compactList(values []string, limit, maxRunes int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	for _, value := range values {
		value = compactFact(value, maxRunes)
		if value == "" {
			continue
		}
		out = append(out, value)
		if len(out) == limit {
			return out
		}
	}
	return out
}

func compactFact(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(cleanLLMText(value)), " ")
	if value == "" || maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}

func firstNonEmpty(values []string, fallback string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return fallback
}
