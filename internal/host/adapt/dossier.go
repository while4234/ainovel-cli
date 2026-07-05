package adapt

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

const (
	CoCreateDossierBatchSize     = 40
	CoCreateDossierPromptVersion = "v1"

	coCreateDossierVersion   = 1
	coCreateDossierMaxTokens = 4096
)

const coCreateDossierSystemPrompt = `You are an adaptation continuity analyst for long Chinese web novels.
You read compact per-chapter source reports, not raw prose. Extract only facts supported by the reports.

Return one JSON object with this shape:
{
  "plot_phase": "brief phase summary for this source range",
  "key_causality": ["major causal chain or irreversible source fact"],
  "major_characters": ["names that matter in this range"],
  "relationship_signals": [{"chapters":[1],"characters":["A","B"],"type":"trust/conflict/romance/etc","summary":"what changed","evidence":"chapter evidence"}],
  "heroine_signals": [{"chapters":[1],"characters":["male lead","heroine"],"type":"interaction/status/milestone","summary":"heroine-relevant beat","evidence":"chapter evidence"}],
  "ambiguity_risks": [{"chapters":[1],"characters":["male lead","side character"],"risk":"possible ambiguity/harem/body-contact risk","evidence":"chapter evidence","severity":"low|medium|high","suggestion":"single-heroine adaptation handling"}],
  "couple_milestones": [{"chapters":[1],"characters":["male lead","heroine"],"type":"meet/ambiguous/confession/couple/etc","summary":"relationship milestone","evidence":"chapter evidence"}],
  "adaptation_notes": ["how to add heroine scenes or remove harem ambiguity without breaking source causality"]
}

Rules:
- Preserve source causality and chapter references.
- Do not invent romance. Mark uncertainty as risk only when supported by reports.
- Focus on heroine presence, side-female ambiguity, confession/like signals, body-contact boundaries, and relationship progress.
- Keep each array compact: usually 3-8 items.`

func EnsureCoCreateDossier(ctx context.Context, deps Deps, manifest *domain.AdaptationSourceManifest, reports []domain.AdaptationSourceReport, emit ProgressEmitter) (*domain.AdaptationCoCreateDossier, error) {
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if manifest == nil || manifest.ChapterCount <= 0 {
		return nil, fmt.Errorf("source manifest is required")
	}
	if len(reports) != manifest.ChapterCount {
		return nil, fmt.Errorf("source reports incomplete: got %d, want %d", len(reports), manifest.ChapterCount)
	}

	current, err := deps.Store.Adaptation.LoadCoCreateDossier()
	if err != nil {
		return nil, fmt.Errorf("load co-create dossier: %w", err)
	}
	if current != nil && store.CoCreateDossierMatchesManifest(*current, *manifest, CoCreateDossierPromptVersion, CoCreateDossierBatchSize) {
		emitAdaptProgress(emit, StageDossier, manifest.ChapterCount, manifest.ChapterCount, "全书改编资料包已存在，跳过生成", nil)
		return current, nil
	}

	emitAdaptProgress(emit, StageDossier, 0, manifest.ChapterCount, "生成全书改编资料包...", nil)
	reportByChapter := make(map[int]domain.AdaptationSourceReport, len(reports))
	for _, report := range reports {
		reportByChapter[report.Chapter] = report
	}

	specs := dossierBatchSpecs(*manifest, CoCreateDossierBatchSize)
	batches := make([]domain.AdaptationCoCreateDossierBatch, 0, len(specs))
	for _, spec := range specs {
		existing, err := deps.Store.Adaptation.LoadCoCreateDossierBatch(spec.Index)
		if err != nil {
			return nil, fmt.Errorf("load co-create dossier batch %d: %w", spec.Index, err)
		}
		if coCreateDossierBatchCurrent(existing, spec) {
			batches = append(batches, *existing)
			emitAdaptProgress(emit, StageDossier, spec.SourceTo, manifest.ChapterCount, fmt.Sprintf("跳过资料包第 %d/%d 批：原书第 %d-%d 章", spec.Index, len(specs), spec.SourceFrom, spec.SourceTo), nil)
			continue
		}

		batchReports := make([]domain.AdaptationSourceReport, 0, spec.SourceTo-spec.SourceFrom+1)
		for chapter := spec.SourceFrom; chapter <= spec.SourceTo; chapter++ {
			report, ok := reportByChapter[chapter]
			if !ok {
				return nil, fmt.Errorf("source report %d missing for co-create dossier", chapter)
			}
			batchReports = append(batchReports, report)
		}
		emitAdaptProgress(emit, StageDossier, spec.SourceFrom, manifest.ChapterCount, fmt.Sprintf("分析资料包第 %d/%d 批：原书第 %d-%d 章", spec.Index, len(specs), spec.SourceFrom, spec.SourceTo), nil)
		batch, err := buildCoCreateDossierBatch(ctx, deps, spec, batchReports, len(specs), emit)
		if err != nil {
			return nil, fmt.Errorf("build co-create dossier batch %d: %w", spec.Index, err)
		}
		if err := deps.Store.Adaptation.SaveCoCreateDossierBatch(batch); err != nil {
			return nil, fmt.Errorf("save co-create dossier batch %d: %w", spec.Index, err)
		}
		batches = append(batches, batch)
		emitAdaptProgress(emit, StageDossier, spec.SourceTo, manifest.ChapterCount, fmt.Sprintf("资料包第 %d/%d 批完成", spec.Index, len(specs)), nil)
	}

	dossier := assembleCoCreateDossier(*manifest, batches)
	if err := deps.Store.Adaptation.SaveCoCreateDossier(dossier); err != nil {
		return nil, fmt.Errorf("save co-create dossier: %w", err)
	}
	emitAdaptProgress(emit, StageDossier, manifest.ChapterCount, manifest.ChapterCount, fmt.Sprintf("全书改编资料包已生成：%d 批 / %d 章", len(batches), manifest.ChapterCount), nil)
	return &dossier, nil
}

type coCreateDossierBatchSpec struct {
	Index           int
	SourceFrom      int
	SourceTo        int
	SourceSignature string
}

func dossierBatchSpecs(manifest domain.AdaptationSourceManifest, batchSize int) []coCreateDossierBatchSpec {
	if batchSize <= 0 {
		batchSize = CoCreateDossierBatchSize
	}
	specs := make([]coCreateDossierBatchSpec, 0, (manifest.ChapterCount+batchSize-1)/batchSize)
	for from, index := 1, 1; from <= manifest.ChapterCount; from, index = from+batchSize, index+1 {
		to := from + batchSize - 1
		if to > manifest.ChapterCount {
			to = manifest.ChapterCount
		}
		specs = append(specs, coCreateDossierBatchSpec{
			Index:           index,
			SourceFrom:      from,
			SourceTo:        to,
			SourceSignature: sourceRangeSignature(manifest, from, to),
		})
	}
	return specs
}

func coCreateDossierBatchCurrent(batch *domain.AdaptationCoCreateDossierBatch, spec coCreateDossierBatchSpec) bool {
	if batch == nil {
		return false
	}
	return batch.Index == spec.Index &&
		batch.SourceFrom == spec.SourceFrom &&
		batch.SourceTo == spec.SourceTo &&
		strings.TrimSpace(batch.SourceSignature) == spec.SourceSignature &&
		strings.TrimSpace(batch.PromptVersion) == CoCreateDossierPromptVersion
}

func buildCoCreateDossierBatch(ctx context.Context, deps Deps, spec coCreateDossierBatchSpec, reports []domain.AdaptationSourceReport, totalBatches int, emit ProgressEmitter) (domain.AdaptationCoCreateDossierBatch, error) {
	userPrompt := buildCoCreateDossierBatchPrompt(spec, reports)
	text, err := generatePlannerText(ctx, deps.LLM, coCreateDossierSystemPrompt, userPrompt, coCreateDossierMaxTokens, emit, spec.Index, totalBatches, "资料包")
	if err != nil {
		return domain.AdaptationCoCreateDossierBatch{}, err
	}
	batch, err := parseCoCreateDossierBatch(text)
	if err != nil {
		return domain.AdaptationCoCreateDossierBatch{}, err
	}
	batch.Index = spec.Index
	batch.SourceFrom = spec.SourceFrom
	batch.SourceTo = spec.SourceTo
	batch.SourceSignature = spec.SourceSignature
	batch.PromptVersion = CoCreateDossierPromptVersion
	batch.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	normalizeCoCreateDossierBatch(&batch)
	return batch, nil
}

func buildCoCreateDossierBatchPrompt(spec coCreateDossierBatchSpec, reports []domain.AdaptationSourceReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Source chapter range: %d-%d\n", spec.SourceFrom, spec.SourceTo)
	fmt.Fprintf(&sb, "Task: extract adaptation co-create dossier facts for this range.\n\n")
	for _, report := range reports {
		fmt.Fprintf(&sb, "## Chapter %d: %s\n", report.Chapter, report.Title)
		fmt.Fprintf(&sb, "Summary: %s\n", clipText(report.Summary, 260))
		writeStringList(&sb, "Characters", report.Characters, 20, 80)
		writeStringList(&sb, "Character facts", report.CharacterFacts, 10, 120)
		writeStringList(&sb, "Key events", report.KeyEvents, 8, 120)
		writeRelationships(&sb, report.Relationships)
		writeStateChanges(&sb, report.StateChanges)
		sb.WriteString("\n")
	}
	return sb.String()
}

func parseCoCreateDossierBatch(text string) (domain.AdaptationCoCreateDossierBatch, error) {
	segments, err := extractPlannerJSONSegments(text)
	if err != nil {
		return domain.AdaptationCoCreateDossierBatch{}, err
	}
	var firstErr error
	for _, segment := range segments {
		batch, err := decodeCoCreateDossierBatchJSON([]byte(segment))
		if err == nil {
			return batch, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return domain.AdaptationCoCreateDossierBatch{}, firstErr
	}
	return domain.AdaptationCoCreateDossierBatch{}, fmt.Errorf("co-create dossier batch has no decodable JSON object")
}

func decodeCoCreateDossierBatchJSON(data []byte) (domain.AdaptationCoCreateDossierBatch, error) {
	var batch domain.AdaptationCoCreateDossierBatch
	if err := json.Unmarshal(data, &batch); err == nil && coCreateDossierBatchHasContent(batch) {
		return batch, nil
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return domain.AdaptationCoCreateDossierBatch{}, err
	}
	for _, key := range []string{"batch", "dossier_batch", "dossierBatch", "result", "data", "output"} {
		raw, ok := envelope[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, &batch); err == nil && coCreateDossierBatchHasContent(batch) {
			return batch, nil
		}
	}
	return domain.AdaptationCoCreateDossierBatch{}, fmt.Errorf("co-create dossier batch missing content")
}

func coCreateDossierBatchHasContent(batch domain.AdaptationCoCreateDossierBatch) bool {
	return strings.TrimSpace(batch.PlotPhase) != "" ||
		len(batch.KeyCausality) > 0 ||
		len(batch.RelationshipSignals) > 0 ||
		len(batch.HeroineSignals) > 0 ||
		len(batch.AmbiguityRisks) > 0 ||
		len(batch.CoupleMilestones) > 0 ||
		len(batch.AdaptationNotes) > 0
}

func normalizeCoCreateDossierBatch(batch *domain.AdaptationCoCreateDossierBatch) {
	if batch == nil {
		return
	}
	batch.PlotPhase = strings.TrimSpace(batch.PlotPhase)
	batch.KeyCausality = limitStrings(trimmedNonEmpty(batch.KeyCausality), 12)
	batch.MajorCharacters = limitStrings(trimmedNonEmpty(batch.MajorCharacters), 30)
	batch.RelationshipSignals = limitSignals(batch.RelationshipSignals, 16)
	batch.HeroineSignals = limitSignals(batch.HeroineSignals, 12)
	batch.AmbiguityRisks = limitRisks(batch.AmbiguityRisks, 12)
	batch.CoupleMilestones = limitSignals(batch.CoupleMilestones, 10)
	batch.AdaptationNotes = limitStrings(trimmedNonEmpty(batch.AdaptationNotes), 12)
}

func assembleCoCreateDossier(manifest domain.AdaptationSourceManifest, batches []domain.AdaptationCoCreateDossierBatch) domain.AdaptationCoCreateDossier {
	sort.SliceStable(batches, func(i, j int) bool {
		return batches[i].Index < batches[j].Index
	})
	mainline := make([]string, 0, len(batches)*3)
	notes := make([]string, 0, len(batches)*3)
	var relationshipSignals, heroineSignals, milestones []domain.AdaptationRelationshipSignal
	var risks []domain.AdaptationRelationshipRisk
	for _, batch := range batches {
		if batch.PlotPhase != "" {
			mainline = append(mainline, fmt.Sprintf("原书第 %d-%d 章：%s", batch.SourceFrom, batch.SourceTo, batch.PlotPhase))
		}
		mainline = append(mainline, batch.KeyCausality...)
		notes = append(notes, batch.AdaptationNotes...)
		relationshipSignals = append(relationshipSignals, batch.RelationshipSignals...)
		heroineSignals = append(heroineSignals, batch.HeroineSignals...)
		milestones = append(milestones, batch.CoupleMilestones...)
		risks = append(risks, batch.AmbiguityRisks...)
	}

	sourceChapters := make([]domain.AdaptationDossierSourceSignature, 0, len(manifest.Chapters))
	for _, ch := range manifest.Chapters {
		sourceChapters = append(sourceChapters, domain.AdaptationDossierSourceSignature{Chapter: ch.Chapter, SHA256: ch.SHA256})
	}
	return domain.AdaptationCoCreateDossier{
		Version:            coCreateDossierVersion,
		PromptVersion:      CoCreateDossierPromptVersion,
		SourcePath:         manifest.SourcePath,
		SourceChapterCount: manifest.ChapterCount,
		SourceSignature:    store.AdaptationSourceSignature(manifest),
		BatchSize:          CoCreateDossierBatchSize,
		GeneratedAt:        time.Now().UTC().Format(time.RFC3339),
		Overview:           fmt.Sprintf("原书共 %d 章，资料包按每 %d 章生成，覆盖全书主线、人物关系、女主戏份、女配暧昧/后宫风险和情侣节点。", manifest.ChapterCount, CoCreateDossierBatchSize),
		Mainline:           limitStrings(dedupeStrings(mainline), 160),
		RelationshipMap:    limitSignals(relationshipSignals, 160),
		HeroineSignals:     limitSignals(heroineSignals, 120),
		AmbiguityRisks:     limitRisks(risks, 120),
		CoupleMilestones:   limitSignals(milestones, 120),
		AdaptationNotes:    limitStrings(dedupeStrings(notes), 120),
		Batches:            batches,
		SourceChapters:     sourceChapters,
	}
}

func sourceRangeSignature(manifest domain.AdaptationSourceManifest, from, to int) string {
	var sources []domain.AdaptationSource
	for _, ch := range manifest.Chapters {
		if ch.Chapter >= from && ch.Chapter <= to {
			sources = append(sources, ch)
		}
	}
	return store.AdaptationSourceSignature(domain.AdaptationSourceManifest{
		ChapterCount: len(sources),
		Chapters:     sources,
	})
}

func writeStringList(sb *strings.Builder, label string, values []string, maxItems, maxRunes int) {
	values = limitStrings(trimmedNonEmpty(values), maxItems)
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(sb, "%s:\n", label)
	for _, value := range values {
		fmt.Fprintf(sb, "- %s\n", clipText(value, maxRunes))
	}
}

func writeRelationships(sb *strings.Builder, values []domain.RelationshipEntry) {
	if len(values) == 0 {
		return
	}
	sb.WriteString("Relationships:\n")
	for i, value := range values {
		if i >= 12 {
			break
		}
		fmt.Fprintf(sb, "- %s / %s: %s\n", value.CharacterA, value.CharacterB, clipText(value.Relation, 120))
	}
}

func writeStateChanges(sb *strings.Builder, values []domain.StateChange) {
	if len(values) == 0 {
		return
	}
	sb.WriteString("State changes:\n")
	for i, value := range values {
		if i >= 12 {
			break
		}
		if strings.TrimSpace(value.Field) != "relation" && !strings.Contains(strings.ToLower(value.Field), "relation") {
			continue
		}
		fmt.Fprintf(sb, "- %s %s: %s -> %s (%s)\n", value.Entity, value.Field, value.OldValue, value.NewValue, clipText(value.Reason, 100))
	}
}

func clipText(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range trimmedNonEmpty(values) {
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func limitStrings(values []string, max int) []string {
	if max > 0 && len(values) > max {
		return values[:max]
	}
	return values
}

func limitSignals(values []domain.AdaptationRelationshipSignal, max int) []domain.AdaptationRelationshipSignal {
	out := make([]domain.AdaptationRelationshipSignal, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value.Summary = strings.TrimSpace(value.Summary)
		value.Type = strings.TrimSpace(value.Type)
		value.Evidence = clipText(value.Evidence, 180)
		value.Characters = limitStrings(trimmedNonEmpty(value.Characters), 8)
		if value.Summary == "" {
			continue
		}
		key := fmt.Sprintf("%v|%v|%s|%s", value.Chapters, value.Characters, strings.ToLower(value.Type), strings.ToLower(value.Summary))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func limitRisks(values []domain.AdaptationRelationshipRisk, max int) []domain.AdaptationRelationshipRisk {
	out := make([]domain.AdaptationRelationshipRisk, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value.Risk = strings.TrimSpace(value.Risk)
		value.Evidence = clipText(value.Evidence, 180)
		value.Severity = strings.TrimSpace(value.Severity)
		value.Suggestion = clipText(value.Suggestion, 180)
		value.Characters = limitStrings(trimmedNonEmpty(value.Characters), 8)
		if value.Risk == "" {
			continue
		}
		key := fmt.Sprintf("%v|%v|%s", value.Chapters, value.Characters, strings.ToLower(value.Risk))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}
