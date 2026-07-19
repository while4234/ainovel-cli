package adapt

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

const SourceAnalysisDiagnosticVersion = 1

type SourceAnalysisIssue struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Recoverable bool   `json:"recoverable"`
}

type SourceAnalysisEstimate struct {
	ReusableChapterReports int `json:"reusable_chapter_reports"`
	ChapterCalls           int `json:"chapter_calls"`
	FoundationCalls        int `json:"foundation_calls"`
	DossierCalls           int `json:"dossier_calls"`
	EstimatedCalls         int `json:"estimated_calls"`
}

// SourceAnalysisDiagnostic is independent from the historical "done" flag.
// It proves that every current source-derived artifact is usable and bound to
// the exact chapter manifest before the UI may call the analysis complete.
type SourceAnalysisDiagnostic struct {
	Version                 int                    `json:"version"`
	Status                  string                 `json:"status"`
	Complete                bool                   `json:"complete"`
	SourceExists            bool                   `json:"source_exists"`
	SourceSignature         string                 `json:"source_signature,omitempty"`
	ChapterCount            int                    `json:"chapter_count"`
	ReportCount             int                    `json:"report_count"`
	FoundationVersion       int                    `json:"foundation_version,omitempty"`
	FoundationPromptVersion string                 `json:"foundation_prompt_version,omitempty"`
	DossierVersion          int                    `json:"dossier_version,omitempty"`
	DossierPromptVersion    string                 `json:"dossier_prompt_version,omitempty"`
	PremisePresent          bool                   `json:"premise_present"`
	CharacterCount          int                    `json:"character_count"`
	CompleteCharacterCount  int                    `json:"complete_character_count"`
	WorldRuleCount          int                    `json:"world_rule_count"`
	RelationshipCount       int                    `json:"relationship_count"`
	RelationshipsReviewed   bool                   `json:"relationships_reviewed"`
	MissingProducts         []string               `json:"missing_products"`
	StaleReasons            []string               `json:"stale_reasons"`
	Issues                  []SourceAnalysisIssue  `json:"issues"`
	Estimate                SourceAnalysisEstimate `json:"estimate"`
}

func DiagnoseSourceAnalysis(st *store.Store, prompts Prompts, modelName string) (SourceAnalysisDiagnostic, error) {
	diagnostic := SourceAnalysisDiagnostic{
		Version:         SourceAnalysisDiagnosticVersion,
		Status:          "missing",
		MissingProducts: []string{},
		StaleReasons:    []string{},
		Issues:          []SourceAnalysisIssue{},
	}
	if st == nil || st.Adaptation == nil {
		return diagnostic, fmt.Errorf("adaptation store is required")
	}

	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		return diagnostic, fmt.Errorf("load source manifest: %w", err)
	}
	if manifest == nil || manifest.ChapterCount <= 0 || len(manifest.Chapters) != manifest.ChapterCount {
		diagnostic.addMissing("source_manifest", "来源章节清单缺失或不完整")
		diagnostic.finish()
		return diagnostic, nil
	}
	diagnostic.ChapterCount = manifest.ChapterCount
	diagnostic.SourceSignature = store.AdaptationSourceSignature(*manifest)
	if info, statErr := os.Stat(strings.TrimSpace(manifest.SourcePath)); statErr == nil && !info.IsDir() {
		diagnostic.SourceExists = true
	} else {
		diagnostic.addMissing("source_file", "原文文件不存在或不可读取")
	}

	reports, err := st.Adaptation.LoadSourceReports()
	if err != nil {
		return diagnostic, fmt.Errorf("load source reports: %w", err)
	}
	reusableReports := reusableReportCount(reports, manifest)
	diagnostic.ReportCount = reusableReports
	if reusableReports != manifest.ChapterCount {
		diagnostic.addMissing("chapter_reports", fmt.Sprintf("逐章报告仅覆盖 %d/%d 章", reusableReports, manifest.ChapterCount))
	}

	foundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		return diagnostic, fmt.Errorf("load source foundation: %w", err)
	}
	if foundation == nil {
		diagnostic.addMissing("source_foundation", "SourceFoundation 缺失")
	} else {
		diagnostic.inspectFoundation(foundation, manifest, reports, prompts, modelName)
	}

	dossier, err := st.Adaptation.LoadCoCreateDossier()
	if err != nil {
		return diagnostic, fmt.Errorf("load co-create dossier: %w", err)
	}
	if dossier == nil {
		diagnostic.addMissing("dossier", "来源 dossier 缺失")
	} else {
		diagnostic.DossierVersion = dossier.Version
		diagnostic.DossierPromptVersion = strings.TrimSpace(dossier.PromptVersion)
		diagnostic.RelationshipCount = len(dossier.RelationshipMap)
		diagnostic.RelationshipsReviewed = dossier.RelationshipMap != nil
		if !store.CoCreateDossierMatchesManifest(*dossier, *manifest, CoCreateDossierPromptVersion, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit) {
			diagnostic.addStale("dossier", "来源 dossier 的版本、提示词或章节签名已过期")
		}
		if dossier.RelationshipMap == nil {
			diagnostic.addMissing("relationships", "关系分析缺失；空数组才表示已分析但未识别到可靠关系")
		}
	}

	diagnostic.Estimate = estimateSourceAnalysis(diagnostic, manifest.ChapterCount, reusableReports)
	diagnostic.finish()
	return diagnostic, nil
}

func (d *SourceAnalysisDiagnostic) inspectFoundation(
	foundation *domain.AdaptationSourceFoundation,
	manifest *domain.AdaptationSourceManifest,
	reports []domain.AdaptationSourceReport,
	prompts Prompts,
	modelName string,
) {
	d.FoundationVersion = foundation.Version
	d.FoundationPromptVersion = strings.TrimSpace(foundation.PromptVersion)
	d.PremisePresent = strings.TrimSpace(foundation.Premise) != ""
	d.CharacterCount = len(foundation.Characters)
	d.CompleteCharacterCount = completeSourceCharacterCount(foundation.Characters)
	d.WorldRuleCount = len(foundation.WorldRules)
	if !d.PremisePresent {
		d.addMissing("premise", "来源故事前提缺失")
	}
	if d.CharacterCount == 0 {
		d.addMissing("characters", "来源角色缺失")
	} else if d.CompleteCharacterCount == 0 {
		d.addMissing("character_details", "来源角色只有姓名，缺少身份、目标、动机或角色弧")
	}
	if d.WorldRuleCount == 0 {
		d.addMissing("world_rules", "来源世界规则缺失")
	}
	if len(reports) != manifest.ChapterCount {
		return
	}
	reportSignature := sourceReportsSignature(reports)
	if !sourceFoundationDiagnosticCurrent(foundation, manifest, reportSignature, prompts.FoundationMerge, modelName) {
		d.addStale("source_foundation", "SourceFoundation 的版本、提示词或来源签名已过期")
	}
}

func sourceFoundationDiagnosticCurrent(
	foundation *domain.AdaptationSourceFoundation,
	manifest *domain.AdaptationSourceManifest,
	reportSignature string,
	foundationPrompt string,
	modelName string,
) bool {
	if !sourceFoundationUsable(foundation) || manifest == nil {
		return false
	}
	if foundation.Version != adaptationSourceFoundationVersion ||
		foundation.SourceChapterCount != manifest.ChapterCount ||
		strings.TrimSpace(foundation.SourceSignature) != store.AdaptationSourceSignature(*manifest) ||
		strings.TrimSpace(foundation.ReportSignature) != strings.TrimSpace(reportSignature) ||
		strings.TrimSpace(foundation.PromptVersion) == "" || foundation.BatchRuneLimit <= 0 {
		return false
	}
	if strings.TrimSpace(foundationPrompt) != "" && foundation.PromptVersion != sourceFoundationPromptSignature(foundationPrompt) {
		return false
	}
	return strings.TrimSpace(modelName) == "" || foundation.BatchRuneLimit == modelprofileFoundationMergeRunes(modelName)
}

func modelprofileFoundationMergeRunes(modelName string) int {
	return Deps{ModelName: modelName}.foundationMergeBatchRunes()
}

func reusableReportCount(reports []domain.AdaptationSourceReport, manifest *domain.AdaptationSourceManifest) int {
	if manifest == nil {
		return 0
	}
	byChapter := make(map[int]domain.AdaptationSourceReport, len(reports))
	for _, report := range reports {
		byChapter[report.Chapter] = report
	}
	count := 0
	for _, source := range manifest.Chapters {
		report, exists := byChapter[source.Chapter]
		if exists && reusableSourceReport(&report, source.SHA256) {
			count++
		}
	}
	return count
}

func completeSourceCharacterCount(characters []domain.Character) int {
	count := 0
	for _, character := range characters {
		if strings.TrimSpace(character.Name) == "" || strings.TrimSpace(character.Role) == "" {
			continue
		}
		if strings.TrimSpace(character.Goal) == "" && strings.TrimSpace(character.Motivation) == "" && strings.TrimSpace(character.Arc) == "" {
			continue
		}
		count++
	}
	return count
}

func estimateSourceAnalysis(diagnostic SourceAnalysisDiagnostic, chapterCount, reusableReports int) SourceAnalysisEstimate {
	estimate := SourceAnalysisEstimate{ReusableChapterReports: reusableReports}
	estimate.ChapterCalls = chapterCount - reusableReports
	if estimate.ChapterCalls < 0 {
		estimate.ChapterCalls = 0
	}
	if containsAny(diagnostic.MissingProducts, "source_foundation", "premise", "characters", "character_details", "world_rules") ||
		containsAny(diagnostic.StaleReasons, "source_foundation") {
		estimate.FoundationCalls = 1
	}
	if containsAny(diagnostic.MissingProducts, "dossier", "relationships") || containsAny(diagnostic.StaleReasons, "dossier") {
		estimate.DossierCalls = 1
	}
	estimate.EstimatedCalls = estimate.ChapterCalls + estimate.FoundationCalls + estimate.DossierCalls
	return estimate
}

func (d *SourceAnalysisDiagnostic) addMissing(code, message string) {
	d.MissingProducts = appendUnique(d.MissingProducts, code)
	d.Issues = append(d.Issues, SourceAnalysisIssue{Code: code, Message: message, Recoverable: code != "source_file"})
}

func (d *SourceAnalysisDiagnostic) addStale(code, message string) {
	d.StaleReasons = appendUnique(d.StaleReasons, code)
	d.Issues = append(d.Issues, SourceAnalysisIssue{Code: code + "_stale", Message: message, Recoverable: true})
}

func (d *SourceAnalysisDiagnostic) finish() {
	sort.Strings(d.MissingProducts)
	sort.Strings(d.StaleReasons)
	d.Complete = d.SourceExists && len(d.MissingProducts) == 0 && len(d.StaleReasons) == 0
	switch {
	case d.Complete:
		d.Status = "complete"
	case d.SourceExists && (d.ReportCount > 0 || d.FoundationVersion > 0 || d.DossierVersion > 0):
		d.Status = "incomplete"
	default:
		d.Status = "missing"
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func containsAny(values []string, targets ...string) bool {
	for _, value := range values {
		for _, target := range targets {
			if value == target {
				return true
			}
		}
	}
	return false
}
