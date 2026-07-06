package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	adaptationRootDir              = "meta/adaptation"
	adaptationBackupDir            = "meta/adaptation_backups"
	adaptationSourceChapterDir     = adaptationRootDir + "/source_chapters"
	adaptationSourceReportDir      = adaptationRootDir + "/source_reports"
	adaptationSourceReportsFile    = adaptationRootDir + "/source_reports.json"
	adaptationSourceFoundationFile = adaptationRootDir + "/source_foundation.json"
	adaptationSourceFoundationDir  = adaptationRootDir + "/source_foundation_batches"
	adaptationCoCreateDossierFile  = adaptationRootDir + "/cocreate_dossier.json"
	adaptationCoCreateBatchDir     = adaptationRootDir + "/cocreate_dossier_batches"
	adaptationCoCreateIntentFile   = adaptationRootDir + "/cocreate_intent.json"
	adaptationCoCreateBriefingFile = adaptationRootDir + "/cocreate_briefing.json"
	adaptationCoCreateBriefingDir  = adaptationRootDir + "/cocreate_briefing_batches"
	adaptationCheckDir             = adaptationRootDir + "/checks"
	adaptationProposalFile         = adaptationRootDir + "/proposal.json"
	adaptationVolumeReviewFile     = adaptationRootDir + "/proposal_volume_review.json"
	adaptationProposalRuntimeFile  = adaptationRootDir + "/proposal_runtime.json"
	adaptationPlanFile             = adaptationRootDir + "/plan.json"
)

// AdaptationStore keeps source-novel snapshots and adaptation validation data.
type AdaptationStore struct{ io *IO }

func NewAdaptationStore(io *IO) *AdaptationStore { return &AdaptationStore{io: io} }

func (s *AdaptationStore) Reset() error {
	return os.RemoveAll(s.io.path(adaptationRootDir))
}

// Backup copies the current adaptation snapshot before destructive maintenance.
func (s *AdaptationStore) Backup(label string) (string, error) {
	sourceRoot := s.io.path(adaptationRootDir)
	if _, err := os.Stat(sourceRoot); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	label = safeAdaptationBackupLabel(label)
	if label == "" {
		label = "snapshot"
	}
	targetRoot := s.io.path(filepath.ToSlash(filepath.Join(
		adaptationBackupDir,
		time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+label,
	)))
	if err := copyAdaptationDir(sourceRoot, targetRoot); err != nil {
		return "", err
	}
	return targetRoot, nil
}

// ResetGenerated removes adaptation artifacts derived from a confirmed brief
// while preserving the analyzed source-novel snapshot.
func (s *AdaptationStore) ResetGenerated() error {
	return s.io.WithWriteLock(func() error {
		err := os.Remove(s.io.path(adaptationPlanFile))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		err = os.Remove(s.io.path(adaptationProposalFile))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		err = os.Remove(s.io.path(adaptationVolumeReviewFile))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		err = s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile)
		if err != nil {
			return err
		}
		return os.RemoveAll(s.io.path(adaptationCheckDir))
	})
}

func (s *AdaptationStore) SaveSourceManifest(manifest domain.AdaptationSourceManifest) error {
	return s.io.WriteJSON(adaptationRootDir+"/source_manifest.json", manifest)
}

func (s *AdaptationStore) LoadSourceManifest() (*domain.AdaptationSourceManifest, error) {
	var manifest domain.AdaptationSourceManifest
	if err := s.io.ReadJSON(adaptationRootDir+"/source_manifest.json", &manifest); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &manifest, nil
}

func (s *AdaptationStore) SaveSourceChapter(chapter int, title, content string) (domain.AdaptationSource, error) {
	if chapter <= 0 {
		return domain.AdaptationSource{}, fmt.Errorf("chapter must be > 0")
	}
	rel := SourceChapterRelPath(chapter)
	content = strings.TrimSpace(content)
	if err := s.io.WriteMarkdown(rel, content); err != nil {
		return domain.AdaptationSource{}, err
	}
	return domain.AdaptationSource{
		Chapter: chapter,
		Title:   strings.TrimSpace(title),
		SHA256:  TextSHA256(content),
		Path:    rel,
		Runes:   utf8.RuneCountInString(content),
	}, nil
}

func (s *AdaptationStore) LoadSourceChapter(chapter int) (string, *domain.AdaptationSource, error) {
	if chapter <= 0 {
		return "", nil, fmt.Errorf("chapter must be > 0")
	}
	manifest, err := s.LoadSourceManifest()
	if err != nil {
		return "", nil, err
	}
	if manifest == nil {
		return "", nil, nil
	}

	var source *domain.AdaptationSource
	for i := range manifest.Chapters {
		if manifest.Chapters[i].Chapter == chapter {
			source = &manifest.Chapters[i]
			break
		}
	}
	if source == nil {
		return "", nil, nil
	}

	rel := source.Path
	if strings.TrimSpace(rel) == "" {
		rel = SourceChapterRelPath(chapter)
	}
	data, err := s.io.ReadFile(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return "", source, nil
		}
		return "", nil, err
	}
	return string(data), source, nil
}

func (s *AdaptationStore) LoadSourceChapterRange(from, to, maxRunes int) (map[int]string, error) {
	if from <= 0 || to < from {
		return nil, fmt.Errorf("invalid source chapter range %d-%d", from, to)
	}
	result := make(map[int]string)
	for ch := from; ch <= to; ch++ {
		text, _, err := s.LoadSourceChapter(ch)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		result[ch] = truncateRunes(text, maxRunes)
	}
	return result, nil
}

func (s *AdaptationStore) SaveSourceReports(reports []domain.AdaptationSourceReport) error {
	return s.io.WriteJSON(adaptationSourceReportsFile, reports)
}

func (s *AdaptationStore) SaveSourceReport(report domain.AdaptationSourceReport) error {
	if report.Chapter <= 0 {
		return fmt.Errorf("chapter must be > 0")
	}
	return s.io.WriteJSON(SourceReportRelPath(report.Chapter), report)
}

func (s *AdaptationStore) LoadSourceReport(chapter int) (*domain.AdaptationSourceReport, error) {
	if chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}
	var report domain.AdaptationSourceReport
	if err := s.io.ReadJSON(SourceReportRelPath(chapter), &report); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if report.Chapter == 0 {
		report.Chapter = chapter
	}
	return &report, nil
}

func (s *AdaptationStore) LoadSourceReports() ([]domain.AdaptationSourceReport, error) {
	dirReports, err := s.loadSourceReportDir()
	if err != nil {
		return nil, err
	}
	if len(dirReports) > 0 {
		return dirReports, nil
	}

	var reports []domain.AdaptationSourceReport
	if err := s.io.ReadJSON(adaptationSourceReportsFile, &reports); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return reports, nil
}

func (s *AdaptationStore) LoadCompleteSourceReports() ([]domain.AdaptationSourceReport, error) {
	manifest, err := s.LoadSourceManifest()
	if err != nil || manifest == nil {
		return nil, err
	}
	if manifest.ChapterCount <= 0 || len(manifest.Chapters) != manifest.ChapterCount {
		return nil, nil
	}

	reports := make([]domain.AdaptationSourceReport, 0, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		report, err := s.LoadSourceReport(source.Chapter)
		if err != nil || report == nil {
			return nil, err
		}
		if !sourceReportMatches(*report, source.SHA256) {
			return nil, nil
		}
		reports = append(reports, *report)
	}
	return reports, nil
}

func (s *AdaptationStore) loadSourceReportDir() ([]domain.AdaptationSourceReport, error) {
	entries, err := os.ReadDir(s.io.path(adaptationSourceReportDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	reports := make([]domain.AdaptationSourceReport, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		var report domain.AdaptationSourceReport
		rel := adaptationSourceReportDir + "/" + entry.Name()
		if err := s.io.ReadJSON(rel, &report); err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	sort.SliceStable(reports, func(i, j int) bool {
		return reports[i].Chapter < reports[j].Chapter
	})
	return reports, nil
}

func sourceReportMatches(report domain.AdaptationSourceReport, sha256 string) bool {
	return strings.TrimSpace(report.SourceSHA256) != "" &&
		report.SourceSHA256 == sha256 &&
		strings.TrimSpace(report.Summary) != "" &&
		len(report.KeyEvents) > 0
}

func (s *AdaptationStore) SaveSourceFoundation(foundation domain.AdaptationSourceFoundation) error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked(adaptationSourceFoundationFile, foundation); err != nil {
			return err
		}
		return os.RemoveAll(s.io.path(adaptationSourceFoundationDir))
	})
}

func (s *AdaptationStore) LoadSourceFoundation() (*domain.AdaptationSourceFoundation, error) {
	var foundation domain.AdaptationSourceFoundation
	if err := s.io.ReadJSON(adaptationSourceFoundationFile, &foundation); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &foundation, nil
}

func (s *AdaptationStore) SaveSourceFoundationBatch(batch domain.AdaptationSourceFoundationBatch) error {
	if batch.Level < 0 {
		return fmt.Errorf("batch level must be >= 0")
	}
	if batch.Index <= 0 {
		return fmt.Errorf("batch index must be > 0")
	}
	return s.io.WriteJSON(SourceFoundationBatchRelPath(batch.Level, batch.Index), batch)
}

func (s *AdaptationStore) LoadSourceFoundationBatch(level, index int) (*domain.AdaptationSourceFoundationBatch, error) {
	if level < 0 {
		return nil, fmt.Errorf("batch level must be >= 0")
	}
	if index <= 0 {
		return nil, fmt.Errorf("batch index must be > 0")
	}
	var batch domain.AdaptationSourceFoundationBatch
	if err := s.io.ReadJSON(SourceFoundationBatchRelPath(level, index), &batch); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if batch.Level == 0 && level > 0 {
		batch.Level = level
	}
	if batch.Index == 0 {
		batch.Index = index
	}
	return &batch, nil
}

func (s *AdaptationStore) ClearSourceFoundationBatches() error {
	return s.io.WithWriteLock(func() error {
		return os.RemoveAll(s.io.path(adaptationSourceFoundationDir))
	})
}

func (s *AdaptationStore) SaveCoCreateDossier(dossier domain.AdaptationCoCreateDossier) error {
	return s.io.WriteJSON(adaptationCoCreateDossierFile, dossier)
}

func (s *AdaptationStore) LoadCoCreateDossier() (*domain.AdaptationCoCreateDossier, error) {
	var dossier domain.AdaptationCoCreateDossier
	if err := s.io.ReadJSON(adaptationCoCreateDossierFile, &dossier); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &dossier, nil
}

func (s *AdaptationStore) SaveCoCreateDossierBatch(batch domain.AdaptationCoCreateDossierBatch) error {
	if batch.Index <= 0 {
		return fmt.Errorf("batch index must be > 0")
	}
	return s.io.WriteJSON(CoCreateDossierBatchRelPath(batch.Index), batch)
}

func (s *AdaptationStore) LoadCoCreateDossierBatch(index int) (*domain.AdaptationCoCreateDossierBatch, error) {
	if index <= 0 {
		return nil, fmt.Errorf("batch index must be > 0")
	}
	var batch domain.AdaptationCoCreateDossierBatch
	if err := s.io.ReadJSON(CoCreateDossierBatchRelPath(index), &batch); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if batch.Index == 0 {
		batch.Index = index
	}
	return &batch, nil
}

func (s *AdaptationStore) LoadCoCreateDossierBatches() ([]domain.AdaptationCoCreateDossierBatch, error) {
	entries, err := os.ReadDir(s.io.path(adaptationCoCreateBatchDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	batches := make([]domain.AdaptationCoCreateDossierBatch, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		var batch domain.AdaptationCoCreateDossierBatch
		rel := adaptationCoCreateBatchDir + "/" + entry.Name()
		if err := s.io.ReadJSON(rel, &batch); err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	sort.SliceStable(batches, func(i, j int) bool {
		return batches[i].Index < batches[j].Index
	})
	return batches, nil
}

func (s *AdaptationStore) SaveCoCreateIntent(intent domain.AdaptationCoCreateIntent) error {
	return s.io.WriteJSON(adaptationCoCreateIntentFile, intent)
}

func (s *AdaptationStore) LoadCoCreateIntent() (*domain.AdaptationCoCreateIntent, error) {
	var intent domain.AdaptationCoCreateIntent
	if err := s.io.ReadJSON(adaptationCoCreateIntentFile, &intent); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &intent, nil
}

func (s *AdaptationStore) SaveCoCreateBriefing(briefing domain.AdaptationCoCreateBriefing) error {
	return s.io.WriteJSON(adaptationCoCreateBriefingFile, briefing)
}

func (s *AdaptationStore) LoadCoCreateBriefing() (*domain.AdaptationCoCreateBriefing, error) {
	var briefing domain.AdaptationCoCreateBriefing
	if err := s.io.ReadJSON(adaptationCoCreateBriefingFile, &briefing); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &briefing, nil
}

func (s *AdaptationStore) SaveCoCreateBriefingBatch(batch domain.AdaptationCoCreateBriefingBatch) error {
	if batch.Index <= 0 {
		return fmt.Errorf("batch index must be > 0")
	}
	return s.io.WriteJSON(CoCreateBriefingBatchRelPath(batch.Index), batch)
}

func (s *AdaptationStore) LoadCoCreateBriefingBatch(index int) (*domain.AdaptationCoCreateBriefingBatch, error) {
	if index <= 0 {
		return nil, fmt.Errorf("batch index must be > 0")
	}
	var batch domain.AdaptationCoCreateBriefingBatch
	if err := s.io.ReadJSON(CoCreateBriefingBatchRelPath(index), &batch); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if batch.Index == 0 {
		batch.Index = index
	}
	return &batch, nil
}

func (s *AdaptationStore) LoadCoCreateBriefingBatches() ([]domain.AdaptationCoCreateBriefingBatch, error) {
	entries, err := os.ReadDir(s.io.path(adaptationCoCreateBriefingDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	batches := make([]domain.AdaptationCoCreateBriefingBatch, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		var batch domain.AdaptationCoCreateBriefingBatch
		rel := adaptationCoCreateBriefingDir + "/" + entry.Name()
		if err := s.io.ReadJSON(rel, &batch); err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	sort.SliceStable(batches, func(i, j int) bool {
		return batches[i].Index < batches[j].Index
	})
	return batches, nil
}

func (s *AdaptationStore) CoCreateBriefingCurrent(promptVersion string, dossierPromptVersion string, intentHash string) (bool, error) {
	manifest, err := s.LoadSourceManifest()
	if err != nil || manifest == nil {
		return false, err
	}
	dossier, err := s.LoadCoCreateDossier()
	if err != nil || dossier == nil {
		return false, err
	}
	briefing, err := s.LoadCoCreateBriefing()
	if err != nil || briefing == nil {
		return false, err
	}
	return CoCreateBriefingMatches(*briefing, *manifest, *dossier, promptVersion, dossierPromptVersion, intentHash), nil
}

func CoCreateBriefingMatches(
	briefing domain.AdaptationCoCreateBriefing,
	manifest domain.AdaptationSourceManifest,
	dossier domain.AdaptationCoCreateDossier,
	promptVersion string,
	dossierPromptVersion string,
	intentHash string,
) bool {
	if strings.TrimSpace(briefing.PromptVersion) != strings.TrimSpace(promptVersion) {
		return false
	}
	if strings.TrimSpace(briefing.DossierPromptVersion) != strings.TrimSpace(dossierPromptVersion) {
		return false
	}
	if strings.TrimSpace(briefing.IntentHash) != strings.TrimSpace(intentHash) {
		return false
	}
	if briefing.SourceChapterCount != manifest.ChapterCount {
		return false
	}
	if briefing.SourceSignature != AdaptationSourceSignature(manifest) {
		return false
	}
	if briefing.DossierBatchCount != len(dossier.Batches) {
		return false
	}
	return true
}

func (s *AdaptationStore) ResolveCoCreateBriefingDecision(decisionID, optionID, customAnswer string) (*domain.AdaptationCoCreateBriefing, error) {
	return s.ResolveCoCreateBriefingDecisions([]domain.AdaptationResolvedDecision{{
		DecisionID:   decisionID,
		OptionID:     optionID,
		CustomAnswer: customAnswer,
	}})
}

func (s *AdaptationStore) ResolveCoCreateBriefingDecisions(decisions []domain.AdaptationResolvedDecision) (*domain.AdaptationCoCreateBriefing, error) {
	if len(decisions) == 0 {
		return nil, fmt.Errorf("decisions are required")
	}
	briefing, err := s.LoadCoCreateBriefing()
	if err != nil {
		return nil, err
	}
	if briefing == nil {
		return nil, fmt.Errorf("co-create briefing is required")
	}
	next := *briefing
	next.Decisions = append([]domain.AdaptationBriefingDecision(nil), briefing.Decisions...)
	next.ResolvedDecisions = append([]domain.AdaptationResolvedDecision(nil), briefing.ResolvedDecisions...)
	for _, item := range decisions {
		decisionID := strings.TrimSpace(item.DecisionID)
		optionID := strings.TrimSpace(item.OptionID)
		customAnswer := strings.TrimSpace(item.CustomAnswer)
		if decisionID == "" {
			return nil, fmt.Errorf("decision_id is required")
		}
		if optionID == "" && customAnswer == "" {
			return nil, fmt.Errorf("option_id or custom_answer is required")
		}
		decision := findBriefingDecision(next, decisionID)
		if decision == nil {
			return nil, fmt.Errorf("decision not found")
		}
		if optionID != "" && !briefingDecisionHasOption(*decision, optionID) {
			return nil, fmt.Errorf("decision option not found")
		}
		resolved := domain.AdaptationResolvedDecision{
			DecisionID:   decisionID,
			OptionID:     optionID,
			CustomAnswer: customAnswer,
			ResolvedAt:   timeNowUTCString(),
		}
		next.ResolvedDecisions = upsertResolvedDecision(next.ResolvedDecisions, resolved)
		markBriefingDecisionResolved(&next, decisionID)
	}
	if err := s.SaveCoCreateBriefing(next); err != nil {
		return nil, err
	}
	return &next, nil
}

func (s *AdaptationStore) CoCreateDossierCurrent(promptVersion string, batchSize int, batchRuneLimit ...int) (bool, error) {
	manifest, err := s.LoadSourceManifest()
	if err != nil || manifest == nil {
		return false, err
	}
	dossier, err := s.LoadCoCreateDossier()
	if err != nil || dossier == nil {
		return false, err
	}
	return CoCreateDossierMatchesManifest(*dossier, *manifest, promptVersion, batchSize, batchRuneLimit...), nil
}

func CoCreateDossierMatchesManifest(dossier domain.AdaptationCoCreateDossier, manifest domain.AdaptationSourceManifest, promptVersion string, batchSize int, batchRuneLimit ...int) bool {
	if batchSize <= 0 {
		return false
	}
	if strings.TrimSpace(dossier.PromptVersion) != strings.TrimSpace(promptVersion) {
		return false
	}
	runeLimit := optionalDossierBatchRuneLimit(batchRuneLimit)
	if runeLimit > 0 && dossier.BatchRuneLimit != runeLimit {
		return false
	}
	if dossier.BatchSize != batchSize || dossier.SourceChapterCount != manifest.ChapterCount {
		return false
	}
	if dossier.SourceSignature != AdaptationSourceSignature(manifest) {
		return false
	}
	specs := AdaptationDossierBatchSpecs(manifest, batchSize, runeLimit)
	if len(dossier.Batches) != len(specs) {
		return false
	}
	batches := append([]domain.AdaptationCoCreateDossierBatch(nil), dossier.Batches...)
	sort.SliceStable(batches, func(i, j int) bool {
		return batches[i].Index < batches[j].Index
	})
	for i, spec := range specs {
		batch := batches[i]
		if batch.Index != spec.Index || batch.SourceFrom != spec.SourceFrom || batch.SourceTo != spec.SourceTo {
			return false
		}
		if strings.TrimSpace(batch.SourceSignature) != spec.SourceSignature {
			return false
		}
	}
	return true
}

type AdaptationDossierBatchSpec struct {
	Index           int
	SourceFrom      int
	SourceTo        int
	SourceSignature string
}

func AdaptationDossierBatchSpecs(manifest domain.AdaptationSourceManifest, batchSize int, batchRuneLimit int) []AdaptationDossierBatchSpec {
	if manifest.ChapterCount <= 0 || batchSize <= 0 {
		return nil
	}
	runesByChapter := make(map[int]int, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		runesByChapter[source.Chapter] = source.Runes
	}
	specs := make([]AdaptationDossierBatchSpec, 0, adaptationDossierBatchCount(manifest.ChapterCount, batchSize))
	from := 1
	index := 1
	batchRunes := 0
	batchChapters := 0
	for chapter := 1; chapter <= manifest.ChapterCount; chapter++ {
		runes := runesByChapter[chapter]
		if runes < 0 {
			runes = 0
		}
		if batchChapters > 0 && (batchChapters >= batchSize || (batchRuneLimit > 0 && batchRunes+runes > batchRuneLimit)) {
			specs = append(specs, adaptationDossierBatchSpec(manifest, index, from, chapter-1))
			index++
			from = chapter
			batchRunes = 0
			batchChapters = 0
		}
		batchRunes += runes
		batchChapters++
	}
	if batchChapters > 0 {
		specs = append(specs, adaptationDossierBatchSpec(manifest, index, from, manifest.ChapterCount))
	}
	return specs
}

func adaptationDossierBatchSpec(manifest domain.AdaptationSourceManifest, index, from, to int) AdaptationDossierBatchSpec {
	return AdaptationDossierBatchSpec{
		Index:           index,
		SourceFrom:      from,
		SourceTo:        to,
		SourceSignature: adaptationDossierSourceRangeSignature(manifest, from, to),
	}
}

func adaptationDossierSourceRangeSignature(manifest domain.AdaptationSourceManifest, from, to int) string {
	var sources []domain.AdaptationSource
	for _, ch := range manifest.Chapters {
		if ch.Chapter >= from && ch.Chapter <= to {
			sources = append(sources, ch)
		}
	}
	return AdaptationSourceSignature(domain.AdaptationSourceManifest{
		ChapterCount: len(sources),
		Chapters:     sources,
	})
}

func optionalDossierBatchRuneLimit(values []int) int {
	if len(values) == 0 || values[0] <= 0 {
		return 0
	}
	return values[0]
}

func AdaptationSourceSignature(manifest domain.AdaptationSourceManifest) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "chapters=%d\n", manifest.ChapterCount)
	for _, ch := range manifest.Chapters {
		fmt.Fprintf(&sb, "%d:%s\n", ch.Chapter, ch.SHA256)
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

func adaptationDossierBatchCount(chapterCount, batchSize int) int {
	if chapterCount <= 0 || batchSize <= 0 {
		return 0
	}
	return (chapterCount + batchSize - 1) / batchSize
}

func (s *AdaptationStore) SavePlan(plan domain.AdaptationPlan) error {
	s.normalizeAdaptationPlan(&plan)
	plan.Status = domain.AdaptationPlanStatusConfirmed
	return s.io.WithWriteLock(func() error {
		if err := s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile); err != nil {
			return err
		}
		if err := s.io.RemoveFileUnlocked(adaptationVolumeReviewFile); err != nil {
			return err
		}
		return s.io.WriteJSONUnlocked(adaptationPlanFile, plan)
	})
}

func (s *AdaptationStore) LoadPlan() (*domain.AdaptationPlan, error) {
	var plan domain.AdaptationPlan
	if err := s.io.ReadJSON(adaptationPlanFile, &plan); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	s.normalizeAdaptationPlan(&plan)
	return &plan, nil
}

func (s *AdaptationStore) SaveProposal(plan domain.AdaptationPlan) error {
	s.normalizeAdaptationPlan(&plan)
	plan.Status = domain.AdaptationPlanStatusProposal
	return s.io.WithWriteLock(func() error {
		if err := s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile); err != nil {
			return err
		}
		if err := s.io.RemoveFileUnlocked(adaptationVolumeReviewFile); err != nil {
			return err
		}
		return s.io.WriteJSONUnlocked(adaptationProposalFile, plan)
	})
}

func (s *AdaptationStore) LoadProposal() (*domain.AdaptationPlan, error) {
	var proposal domain.AdaptationPlan
	if err := s.io.ReadJSON(adaptationProposalFile, &proposal); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	s.normalizeAdaptationPlan(&proposal)
	proposal.Status = domain.AdaptationPlanStatusProposal
	return &proposal, nil
}

func (s *AdaptationStore) SaveVolumeReview(review domain.AdaptationVolumeReview) error {
	review.Status = domain.AdaptationPlanStatusVolumeReview
	return s.io.WithWriteLock(func() error {
		if err := s.io.RemoveFileUnlocked(adaptationProposalFile); err != nil {
			return err
		}
		if err := s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile); err != nil {
			return err
		}
		return s.io.WriteJSONUnlocked(adaptationVolumeReviewFile, review)
	})
}

func (s *AdaptationStore) LoadVolumeReview() (*domain.AdaptationVolumeReview, error) {
	var review domain.AdaptationVolumeReview
	if err := s.io.ReadJSON(adaptationVolumeReviewFile, &review); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	review.Status = domain.AdaptationPlanStatusVolumeReview
	return &review, nil
}

func (s *AdaptationStore) ClearVolumeReview() error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.RemoveFileUnlocked(adaptationVolumeReviewFile); err != nil {
			return err
		}
		return s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile)
	})
}

func (s *AdaptationStore) SaveProposalRuntime(runtime domain.AdaptationProposalRuntime) error {
	return s.io.WriteJSON(adaptationProposalRuntimeFile, runtime)
}

func (s *AdaptationStore) LoadProposalRuntime() (*domain.AdaptationProposalRuntime, error) {
	var runtime domain.AdaptationProposalRuntime
	if err := s.io.ReadJSON(adaptationProposalRuntimeFile, &runtime); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &runtime, nil
}

func (s *AdaptationStore) ClearProposalRuntime() error {
	return s.io.RemoveFile(adaptationProposalRuntimeFile)
}

func (s *AdaptationStore) ClearProposal() error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.RemoveFileUnlocked(adaptationProposalFile); err != nil {
			return err
		}
		if err := s.io.RemoveFileUnlocked(adaptationVolumeReviewFile); err != nil {
			return err
		}
		return s.io.RemoveFileUnlocked(adaptationProposalRuntimeFile)
	})
}

func (s *AdaptationStore) Active() bool {
	plan, err := s.LoadPlan()
	return err == nil && plan != nil && plan.Status == domain.AdaptationPlanStatusConfirmed
}

func (s *AdaptationStore) SaveCheck(check domain.AdaptationCheck) error {
	if check.Chapter <= 0 {
		return fmt.Errorf("chapter must be > 0")
	}
	return s.io.WriteJSON(checkRelPath(check.Chapter), check)
}

func (s *AdaptationStore) LoadCheck(chapter int) (*domain.AdaptationCheck, error) {
	if chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0")
	}
	var check domain.AdaptationCheck
	if err := s.io.ReadJSON(checkRelPath(chapter), &check); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &check, nil
}

func (s *AdaptationStore) HasPassingCheck(chapter int, draftSHA256 string) (bool, *domain.AdaptationCheck, error) {
	check, err := s.LoadCheck(chapter)
	if err != nil || check == nil {
		return false, check, err
	}
	return check.Passed && check.DraftSHA256 == draftSHA256, check, nil
}

func SourceChapterRelPath(chapter int) string {
	return fmt.Sprintf("%s/%04d.md", adaptationSourceChapterDir, chapter)
}

func SourceReportRelPath(chapter int) string {
	return fmt.Sprintf("%s/%04d.json", adaptationSourceReportDir, chapter)
}

func CoCreateDossierBatchRelPath(index int) string {
	return fmt.Sprintf("%s/%04d.json", adaptationCoCreateBatchDir, index)
}

func SourceFoundationBatchRelPath(level, index int) string {
	return fmt.Sprintf("%s/level_%02d_batch_%04d.json", adaptationSourceFoundationDir, level, index)
}

func CoCreateBriefingBatchRelPath(index int) string {
	return fmt.Sprintf("%s/%04d.json", adaptationCoCreateBriefingDir, index)
}

func findBriefingDecision(briefing domain.AdaptationCoCreateBriefing, decisionID string) *domain.AdaptationBriefingDecision {
	for i := range briefing.Decisions {
		if strings.TrimSpace(briefing.Decisions[i].ID) == decisionID {
			return &briefing.Decisions[i]
		}
	}
	return nil
}

func briefingDecisionHasOption(decision domain.AdaptationBriefingDecision, optionID string) bool {
	for _, option := range decision.Options {
		if strings.TrimSpace(option.ID) == optionID {
			return true
		}
	}
	return false
}

func upsertResolvedDecision(values []domain.AdaptationResolvedDecision, resolved domain.AdaptationResolvedDecision) []domain.AdaptationResolvedDecision {
	out := append([]domain.AdaptationResolvedDecision(nil), values...)
	for i := range out {
		if strings.TrimSpace(out[i].DecisionID) == strings.TrimSpace(resolved.DecisionID) {
			out[i] = resolved
			return out
		}
	}
	return append(out, resolved)
}

func markBriefingDecisionResolved(briefing *domain.AdaptationCoCreateBriefing, decisionID string) {
	if briefing == nil {
		return
	}
	for i := range briefing.Decisions {
		if strings.TrimSpace(briefing.Decisions[i].ID) == decisionID {
			briefing.Decisions[i].Status = "resolved"
		}
	}
	for i := range briefing.Batches {
		for j := range briefing.Batches[i].DecisionQuestions {
			if strings.TrimSpace(briefing.Batches[i].DecisionQuestions[j].ID) == decisionID {
				briefing.Batches[i].DecisionQuestions[j].Status = "resolved"
			}
		}
	}
}

func timeNowUTCString() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func safeAdaptationBackupLabel(label string) string {
	label = strings.TrimSpace(label)
	replacer := strings.NewReplacer(
		"\\", "-",
		"/", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
		" ", "-",
	)
	label = replacer.Replace(label)
	label = strings.Trim(label, ".-")
	if len(label) > 80 {
		label = label[:80]
	}
	return label
}

func copyAdaptationDir(sourceRoot, targetRoot string) error {
	sourceRoot = filepath.Clean(sourceRoot)
	targetRoot = filepath.Clean(targetRoot)
	if sourceRoot == targetRoot {
		return fmt.Errorf("adaptation backup target must differ from source")
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(targetRoot, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyAdaptationFile(path, target, info.Mode().Perm())
	})
}

func copyAdaptationFile(source, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func TextSHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func checkRelPath(chapter int) string {
	return fmt.Sprintf("%s/%04d.json", adaptationCheckDir, chapter)
}

func truncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

func (s *AdaptationStore) normalizeAdaptationPlan(plan *domain.AdaptationPlan) {
	manifest, _ := s.LoadSourceManifest()
	normalizeAdaptationPlan(plan, manifest)
}

func normalizeAdaptationPlan(plan *domain.AdaptationPlan, manifest *domain.AdaptationSourceManifest) {
	if plan == nil {
		return
	}
	plan.Granularity = domain.NormalizeAdaptationGranularity(plan.Granularity)
	plan.Status = domain.NormalizeAdaptationPlanStatus(plan.Status)
	plan.RewritePolicy = domain.AdaptationRewritePolicyForGranularity(plan.Granularity)
	deriveBudgets := shouldDeriveAdaptationBudgets(plan)
	tolerance := plan.WordTolerance
	if tolerance <= 0 && deriveBudgets {
		tolerance = defaultAdaptationWordTolerance
		plan.WordTolerance = tolerance
	}
	sourceRunes := adaptationSourceRunesByChapter(manifest)
	for i := range plan.Chapters {
		normalizeAdaptationChapterPlan(&plan.Chapters[i], tolerance, sourceRunes, deriveBudgets)
	}
	plan.Volumes = normalizeAdaptationVolumes(plan.Volumes, len(plan.Chapters))
	if deriveBudgets {
		normalizeAdaptationPlanTotals(plan)
	}
}

func normalizeAdaptationVolumes(volumes []domain.AdaptationVolumePlan, chapterCount int) []domain.AdaptationVolumePlan {
	if len(volumes) == 0 || chapterCount <= 0 {
		return nil
	}
	out := make([]domain.AdaptationVolumePlan, 0, len(volumes))
	for _, volume := range volumes {
		if volume.TargetFrom <= 0 || volume.TargetTo < volume.TargetFrom || volume.TargetTo > chapterCount {
			continue
		}
		if volume.Index <= 0 {
			volume.Index = len(out) + 1
		}
		volume.Title = strings.TrimSpace(volume.Title)
		volume.Theme = strings.TrimSpace(volume.Theme)
		volume.Goal = strings.TrimSpace(volume.Goal)
		volume.Summary = strings.TrimSpace(volume.Summary)
		if volume.Title == "" {
			volume.Title = fmt.Sprintf("第 %d-%d 章", volume.TargetFrom, volume.TargetTo)
		}
		out = append(out, volume)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetFrom == out[j].TargetFrom {
			return out[i].Index < out[j].Index
		}
		return out[i].TargetFrom < out[j].TargetFrom
	})
	for i := range out {
		if out[i].Index <= 0 {
			out[i].Index = i + 1
		}
	}
	return out
}

func normalizeAdaptationChapterPlan(chapter *domain.AdaptationChapterPlan, tolerance float64, sourceRunes map[int]int, deriveBudgets bool) {
	if chapter == nil {
		return
	}
	chapter.OutlineEntry.Chapter = chapter.Chapter
	chapter.OutlineEntry.Title = chapter.Title

	if chapter.WordBudget == nil {
		if chapter.SourceRunes > 0 || chapter.TargetRunes > 0 || chapter.TargetMinRunes > 0 || chapter.TargetMaxRunes > 0 {
			chapter.WordBudget = &domain.AdaptationChapterWordBudget{}
		}
	}
	if deriveBudgets && chapter.SourceRunes <= 0 {
		chapter.SourceRunes = sumAdaptationSourceRunes(chapter.SourceChapters, sourceRunes)
	}
	if deriveBudgets && chapter.SourceRunes <= 0 && chapter.SourceRange.From > 0 && chapter.SourceRange.To >= chapter.SourceRange.From {
		for sourceChapter := chapter.SourceRange.From; sourceChapter <= chapter.SourceRange.To; sourceChapter++ {
			chapter.SourceRunes += sourceRunes[sourceChapter]
		}
	}
	if deriveBudgets && chapter.TargetRunes <= 0 && chapter.SourceRunes > 0 {
		chapter.TargetRunes = chapter.SourceRunes
	}
	if chapter.TargetMinRunes <= 0 && chapter.TargetMaxRunes <= 0 && chapter.TargetRunes > 0 {
		chapter.TargetMinRunes, chapter.TargetMaxRunes = adaptationRuneRange(chapter.TargetRunes, tolerance)
	}
	if chapter.WordBudget == nil && (chapter.SourceRunes > 0 || chapter.TargetRunes > 0 || chapter.TargetMinRunes > 0 || chapter.TargetMaxRunes > 0) {
		chapter.WordBudget = &domain.AdaptationChapterWordBudget{}
	}
	if chapter.WordBudget == nil {
		return
	}
	if chapter.WordBudget.SourceRunes <= 0 {
		chapter.WordBudget.SourceRunes = chapter.SourceRunes
	}
	if chapter.WordBudget.TargetRunes <= 0 {
		chapter.WordBudget.TargetRunes = chapter.TargetRunes
	}
	if chapter.WordBudget.MinRunes <= 0 {
		chapter.WordBudget.MinRunes = chapter.TargetMinRunes
	}
	if chapter.WordBudget.MaxRunes <= 0 {
		chapter.WordBudget.MaxRunes = chapter.TargetMaxRunes
	}
	if chapter.WordBudget.Tolerance <= 0 {
		chapter.WordBudget.Tolerance = tolerance
	}
	if chapter.SourceRunes <= 0 {
		chapter.SourceRunes = chapter.WordBudget.SourceRunes
	}
	if chapter.TargetRunes <= 0 {
		chapter.TargetRunes = chapter.WordBudget.TargetRunes
	}
	if chapter.TargetMinRunes <= 0 {
		chapter.TargetMinRunes = chapter.WordBudget.MinRunes
	}
	if chapter.TargetMaxRunes <= 0 {
		chapter.TargetMaxRunes = chapter.WordBudget.MaxRunes
	}
}

const defaultAdaptationWordTolerance = 0.15

func shouldDeriveAdaptationBudgets(plan *domain.AdaptationPlan) bool {
	if plan == nil {
		return false
	}
	if plan.RewritePolicy != domain.AdaptationRewritePreserveDetails {
		return true
	}
	if plan.WordTolerance > 0 ||
		plan.SourceTotalRunes > 0 ||
		plan.TargetTotalRunes > 0 ||
		plan.TargetMinRunes > 0 ||
		plan.TargetMaxRunes > 0 {
		return true
	}
	for _, chapter := range plan.Chapters {
		if chapter.WordBudget != nil ||
			chapter.SourceRunes > 0 ||
			chapter.TargetRunes > 0 ||
			chapter.TargetMinRunes > 0 ||
			chapter.TargetMaxRunes > 0 {
			return true
		}
	}
	return false
}

func normalizeAdaptationPlanTotals(plan *domain.AdaptationPlan) {
	if plan == nil {
		return
	}
	sourceTotal := 0
	targetTotal := 0
	targetMin := 0
	targetMax := 0
	for _, chapter := range plan.Chapters {
		sourceTotal += chapter.SourceRunes
		targetTotal += chapter.TargetRunes
		targetMin += chapter.TargetMinRunes
		targetMax += chapter.TargetMaxRunes
	}
	if plan.SourceTotalRunes <= 0 {
		plan.SourceTotalRunes = sourceTotal
	}
	if plan.TargetTotalRunes <= 0 {
		plan.TargetTotalRunes = targetTotal
	}
	if plan.TargetMinRunes <= 0 {
		plan.TargetMinRunes = targetMin
	}
	if plan.TargetMaxRunes <= 0 {
		plan.TargetMaxRunes = targetMax
	}
	if plan.TargetMinRunes <= 0 && plan.TargetMaxRunes <= 0 && plan.TargetTotalRunes > 0 {
		plan.TargetMinRunes, plan.TargetMaxRunes = adaptationRuneRange(plan.TargetTotalRunes, plan.WordTolerance)
	}
}

func adaptationSourceRunesByChapter(manifest *domain.AdaptationSourceManifest) map[int]int {
	if manifest == nil {
		return nil
	}
	out := make(map[int]int, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		if source.Chapter > 0 && source.Runes > 0 {
			out[source.Chapter] = source.Runes
		}
	}
	return out
}

func sumAdaptationSourceRunes(chapters []int, sourceRunes map[int]int) int {
	total := 0
	for _, chapter := range chapters {
		total += sourceRunes[chapter]
	}
	return total
}

func adaptationRuneRange(target int, tolerance float64) (int, int) {
	if target <= 0 {
		return 0, 0
	}
	if tolerance <= 0 {
		tolerance = defaultAdaptationWordTolerance
	}
	minRunes := int(math.Round(float64(target) * (1 - tolerance)))
	maxRunes := int(math.Round(float64(target) * (1 + tolerance)))
	if minRunes < 1 {
		minRunes = 1
	}
	if maxRunes < minRunes {
		maxRunes = minRunes
	}
	return minRunes, maxRunes
}
