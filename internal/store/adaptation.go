package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	adaptationRootDir              = "meta/adaptation"
	adaptationSourceChapterDir     = adaptationRootDir + "/source_chapters"
	adaptationSourceReportDir      = adaptationRootDir + "/source_reports"
	adaptationSourceReportsFile    = adaptationRootDir + "/source_reports.json"
	adaptationSourceFoundationFile = adaptationRootDir + "/source_foundation.json"
	adaptationCheckDir             = adaptationRootDir + "/checks"
	adaptationProposalFile         = adaptationRootDir + "/proposal.json"
	adaptationPlanFile             = adaptationRootDir + "/plan.json"
)

// AdaptationStore keeps source-novel snapshots and adaptation validation data.
type AdaptationStore struct{ io *IO }

func NewAdaptationStore(io *IO) *AdaptationStore { return &AdaptationStore{io: io} }

func (s *AdaptationStore) Reset() error {
	return os.RemoveAll(s.io.path(adaptationRootDir))
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
	return s.io.WriteJSON(adaptationSourceFoundationFile, foundation)
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

func (s *AdaptationStore) SavePlan(plan domain.AdaptationPlan) error {
	normalizeAdaptationPlan(&plan)
	plan.Status = domain.AdaptationPlanStatusConfirmed
	return s.io.WriteJSON(adaptationPlanFile, plan)
}

func (s *AdaptationStore) LoadPlan() (*domain.AdaptationPlan, error) {
	var plan domain.AdaptationPlan
	if err := s.io.ReadJSON(adaptationPlanFile, &plan); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	normalizeAdaptationPlan(&plan)
	return &plan, nil
}

func (s *AdaptationStore) SaveProposal(plan domain.AdaptationPlan) error {
	normalizeAdaptationPlan(&plan)
	plan.Status = domain.AdaptationPlanStatusProposal
	return s.io.WriteJSON(adaptationProposalFile, plan)
}

func (s *AdaptationStore) LoadProposal() (*domain.AdaptationPlan, error) {
	var proposal domain.AdaptationPlan
	if err := s.io.ReadJSON(adaptationProposalFile, &proposal); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	normalizeAdaptationPlan(&proposal)
	proposal.Status = domain.AdaptationPlanStatusProposal
	return &proposal, nil
}

func (s *AdaptationStore) ClearProposal() error {
	err := os.Remove(s.io.path(adaptationProposalFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

func normalizeAdaptationPlan(plan *domain.AdaptationPlan) {
	if plan == nil {
		return
	}
	plan.Granularity = domain.NormalizeAdaptationGranularity(plan.Granularity)
	plan.Status = domain.NormalizeAdaptationPlanStatus(plan.Status)
	plan.RewritePolicy = domain.AdaptationRewritePolicyForGranularity(plan.Granularity)
}
