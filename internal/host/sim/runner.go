package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
)

const maxSourceRunes = 60000

func Run(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	if deps.Store == nil || deps.LLM == nil {
		return nil, fmt.Errorf("deps incomplete")
	}
	if strings.TrimSpace(opts.SourceDir) == "" {
		return nil, fmt.Errorf("source dir is required")
	}

	events := make(chan Event, 32)
	go func() {
		defer close(events)
		emit := func(stage Stage, current, total int, msg string, err error) {
			ev := Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg, Err: err}
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		}

		emit(StageScan, 0, 0, "扫描 simulate 语料...", nil)
		sources, err := scanSources(opts.SourceDir)
		if err != nil {
			emit(StageError, 0, 0, "扫描 simulate 目录失败", err)
			return
		}
		if len(sources) == 0 {
			emit(StageError, 0, 0, "simulate 目录中没有可分析的 .txt/.md/.markdown 文件", fmt.Errorf("no simulation sources"))
			return
		}

		existing, err := deps.Store.Simulation.Load()
		if err != nil {
			emit(StageError, 0, len(sources), "读取既有画像失败", err)
			return
		}
		pending := pendingSources(existing, sources)
		if len(pending) == 0 {
			if !profileNeedsSynthesis(existing) {
				emit(StageDone, 0, len(sources), "画像已是最新，未发现新增或变更文章", nil)
				return
			}
			emit(StageMerge, len(sources), len(sources), "逐篇分析已完成，继续合成仿写画像...", nil)
		}

		reports := make([]domain.SimulationSourceReport, 0, len(pending))
		for i, source := range pending {
			if err := ctx.Err(); err != nil {
				emit(StageError, i, len(pending), "用户取消画像分析", err)
				return
			}
			emit(StageAnalyze, i+1, len(pending), fmt.Sprintf("分析仿写语料 %d/%d：%s", i+1, len(pending), source.RelativePath), nil)
			report, err := analyzeSourceWithOptions(ctx, deps.LLM, deps.Prompts.Source, source, structuredJSONCallOptions{
				OnRetry: func(ev structuredJSONRetryEvent) {
					emit(StageAnalyze, i+1, len(pending), fmt.Sprintf("重试 %d/%d：%v", ev.Attempt, ev.MaxAttempts, ev.Err), ev.Err)
				},
			})
			if err != nil {
				emit(StageError, i+1, len(pending), "语料分析失败", err)
				return
			}
			reports = append(reports, *report)
			profile := buildProfile(existing, opts.SourceDir, []scannedSource{source}, []domain.SimulationSourceReport{*report}, existingSynthesis(existing), time.Now())
			if err := deps.Store.Simulation.Save(profile); err != nil {
				emit(StageError, i+1, len(pending), "保存逐篇分析进度失败", err)
				return
			}
			existing = &profile
		}

		allReports := mergeSourceReports(existing, reports)
		mergeCurrent, mergeTotal := len(pending), len(pending)
		if len(pending) == 0 {
			mergeCurrent, mergeTotal = len(allReports), len(allReports)
		}
		emit(StageMerge, mergeCurrent, mergeTotal, "合并仿写画像...", nil)
		synthesis, err := mergeSynthesisWithOptions(ctx, deps.LLM, deps.Prompts.Merge, existing, allReports, structuredJSONCallOptions{
			OnRetry: func(ev structuredJSONRetryEvent) {
				emit(StageMerge, mergeCurrent, mergeTotal, fmt.Sprintf("重试 %d/%d：%v", ev.Attempt, ev.MaxAttempts, ev.Err), ev.Err)
			},
		})
		if err != nil {
			emit(StageError, mergeCurrent, mergeTotal, "画像合并失败", err)
			return
		}
		profile := buildProfile(existing, opts.SourceDir, nil, nil, *synthesis, time.Now())
		if err := deps.Store.Simulation.Save(profile); err != nil {
			emit(StageError, mergeCurrent, mergeTotal, "保存仿写画像失败", err)
			return
		}
		emit(StageDone, len(pending), len(pending), fmt.Sprintf("仿写画像已更新：新增/变更 %d 篇，累计 %d 篇", len(pending), len(profile.Corpus.Sources)), nil)
	}()
	return events, nil
}

func AnalyzeSource(ctx context.Context, llm LLMChat, systemPrompt string, source scannedSource) (*domain.SimulationSourceReport, error) {
	return analyzeSourceWithOptions(ctx, llm, systemPrompt, source, structuredJSONCallOptions{})
}

func analyzeSourceWithOptions(ctx context.Context, llm LLMChat, systemPrompt string, source scannedSource, opts structuredJSONCallOptions) (*domain.SimulationSourceReport, error) {
	if strings.TrimSpace(systemPrompt) == "" {
		return nil, fmt.Errorf("source prompt is required")
	}
	messages := []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(buildSourceUserPrompt(source)),
	}
	report, err := runStructuredJSONCall(ctx, llm, messages, func(text string) (domain.SimulationSourceReport, error) {
		var report domain.SimulationSourceReport
		if err := parseJSONPayload(text, &report); err != nil {
			return report, fmt.Errorf("parse source report %s: %w", source.RelativePath, err)
		}
		if strings.TrimSpace(report.Summary) == "" {
			return report, fmt.Errorf("source report %s: summary is required", source.RelativePath)
		}
		return report, nil
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("analyze source %s: %w", source.RelativePath, err)
	}
	now := time.Now().Format(time.RFC3339)
	report.RelativePath = source.RelativePath
	report.SHA256 = source.SHA256
	report.Fingerprint = source.Fingerprint
	report.AnalyzedAt = now
	return &report, nil
}

func MergeSynthesis(ctx context.Context, llm LLMChat, systemPrompt string, existing *domain.SimulationProfile, reports []domain.SimulationSourceReport) (*domain.SimulationSynthesis, error) {
	return mergeSynthesisWithOptions(ctx, llm, systemPrompt, existing, reports, structuredJSONCallOptions{})
}

func mergeSynthesisWithOptions(ctx context.Context, llm LLMChat, systemPrompt string, existing *domain.SimulationProfile, reports []domain.SimulationSourceReport, opts structuredJSONCallOptions) (*domain.SimulationSynthesis, error) {
	if strings.TrimSpace(systemPrompt) == "" {
		return nil, fmt.Errorf("merge prompt is required")
	}
	messages := []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(buildMergeUserPrompt(existing, reports)),
	}
	synthesis, err := runStructuredJSONCall(ctx, llm, messages, func(text string) (domain.SimulationSynthesis, error) {
		var synthesis domain.SimulationSynthesis
		if err := parseJSONPayload(text, &synthesis); err != nil {
			return synthesis, fmt.Errorf("parse synthesis: %w", err)
		}
		return synthesis, nil
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("merge profile: %w", err)
	}
	return &synthesis, nil
}

func pendingSources(existing *domain.SimulationProfile, sources []scannedSource) []scannedSource {
	if existing == nil {
		return sources
	}
	known := make(map[string]struct{}, len(existing.SourceReports))
	for _, report := range existing.SourceReports {
		if strings.TrimSpace(report.Summary) == "" {
			continue
		}
		fingerprint := strings.TrimSpace(report.Fingerprint)
		if fingerprint == "" && report.RelativePath != "" && report.SHA256 != "" {
			fingerprint = domain.SimulationSourceFingerprint(report.RelativePath, report.SHA256)
		}
		if fingerprint != "" {
			known[fingerprint] = struct{}{}
		}
	}
	var pending []scannedSource
	for _, source := range sources {
		if _, ok := known[source.Fingerprint]; ok {
			continue
		}
		pending = append(pending, source)
	}
	return pending
}

func existingSynthesis(existing *domain.SimulationProfile) domain.SimulationSynthesis {
	if existing == nil {
		return domain.SimulationSynthesis{}
	}
	return existing.Synthesis
}

func profileNeedsSynthesis(existing *domain.SimulationProfile) bool {
	return existing != nil && len(existing.SourceReports) > 0 && synthesisIsEmpty(existing.Synthesis)
}

func synthesisIsEmpty(s domain.SimulationSynthesis) bool {
	return len(s.Style.NarrativeVoice) == 0 &&
		len(s.Style.SentenceRhythm) == 0 &&
		len(s.Style.ProseTexture) == 0 &&
		len(s.Style.Perspective) == 0 &&
		len(s.Style.Mood) == 0 &&
		len(s.Style.DoNotCopy) == 0 &&
		len(s.Lexicon.CommonWords) == 0 &&
		len(s.Lexicon.EmotionWords) == 0 &&
		len(s.Lexicon.SceneWords) == 0 &&
		len(s.Lexicon.TransitionWords) == 0 &&
		len(s.Lexicon.SignaturePhrases) == 0 &&
		len(s.PlotDesign.OpeningPatterns) == 0 &&
		len(s.PlotDesign.EscalationPatterns) == 0 &&
		len(s.PlotDesign.TurningPointPatterns) == 0 &&
		len(s.PlotDesign.PayoffPatterns) == 0 &&
		len(s.HookDesign.HookTypes) == 0 &&
		len(s.HookDesign.Placement) == 0 &&
		len(s.HookDesign.CliffhangerPatterns) == 0 &&
		len(s.HookDesign.PayoffRules) == 0 &&
		len(s.PacingDensity.SceneDensity) == 0 &&
		len(s.PacingDensity.InformationRelease) == 0 &&
		len(s.PacingDensity.DialogueActionRatio) == 0 &&
		len(s.PacingDensity.CompressionRules) == 0 &&
		len(s.ReaderEngagement.Methods) == 0 &&
		len(s.ReaderEngagement.EmotionalDrivers) == 0 &&
		len(s.ReaderEngagement.ProgressionRewards) == 0 &&
		len(s.ReaderEngagement.AntiPatterns) == 0 &&
		len(s.RoleGuidance.Coordinator) == 0 &&
		len(s.RoleGuidance.Architect) == 0 &&
		len(s.RoleGuidance.Writer) == 0 &&
		len(s.RoleGuidance.Editor) == 0
}

func buildProfile(
	existing *domain.SimulationProfile,
	sourceDir string,
	pending []scannedSource,
	reports []domain.SimulationSourceReport,
	synthesis domain.SimulationSynthesis,
	now time.Time,
) domain.SimulationProfile {
	stamp := now.Format(time.RFC3339)
	profile := domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		CreatedAt: stamp,
		UpdatedAt: stamp,
		Corpus: domain.SimulationCorpusManifest{
			SourceDir: filepath.ToSlash(sourceDir),
		},
		Synthesis: synthesis,
	}
	if existing != nil {
		profile.CreatedAt = existing.CreatedAt
		if profile.CreatedAt == "" {
			profile.CreatedAt = stamp
		}
		profile.Corpus.Sources = append(profile.Corpus.Sources, existing.Corpus.Sources...)
		profile.SourceReports = append(profile.SourceReports, existing.SourceReports...)
	}

	for i, source := range pending {
		source.AnalyzedAt = stamp
		profile.Corpus.Sources = replaceSourceByPath(profile.Corpus.Sources, source.SimulationSource)
		if i < len(reports) {
			report := reports[i]
			report.AnalyzedAt = stamp
			profile.SourceReports = replaceReportByPath(profile.SourceReports, report)
		}
	}
	sortProfile(&profile)
	return profile
}

func mergeSourceReports(existing *domain.SimulationProfile, reports []domain.SimulationSourceReport) []domain.SimulationSourceReport {
	var merged []domain.SimulationSourceReport
	if existing != nil {
		merged = append(merged, existing.SourceReports...)
	}
	for _, report := range reports {
		merged = replaceReportByPath(merged, report)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].RelativePath == merged[j].RelativePath {
			return merged[i].Fingerprint < merged[j].Fingerprint
		}
		return merged[i].RelativePath < merged[j].RelativePath
	})
	return merged
}

func replaceSourceByPath(sources []domain.SimulationSource, next domain.SimulationSource) []domain.SimulationSource {
	out := sources[:0]
	for _, source := range sources {
		if source.RelativePath == next.RelativePath {
			continue
		}
		out = append(out, source)
	}
	return append(out, next)
}

func replaceReportByPath(reports []domain.SimulationSourceReport, next domain.SimulationSourceReport) []domain.SimulationSourceReport {
	out := reports[:0]
	for _, report := range reports {
		if report.RelativePath == next.RelativePath {
			continue
		}
		out = append(out, report)
	}
	return append(out, next)
}

func sortProfile(profile *domain.SimulationProfile) {
	sort.Slice(profile.Corpus.Sources, func(i, j int) bool {
		if profile.Corpus.Sources[i].RelativePath == profile.Corpus.Sources[j].RelativePath {
			return profile.Corpus.Sources[i].Fingerprint < profile.Corpus.Sources[j].Fingerprint
		}
		return profile.Corpus.Sources[i].RelativePath < profile.Corpus.Sources[j].RelativePath
	})
	sort.Slice(profile.SourceReports, func(i, j int) bool {
		if profile.SourceReports[i].RelativePath == profile.SourceReports[j].RelativePath {
			return profile.SourceReports[i].Fingerprint < profile.SourceReports[j].Fingerprint
		}
		return profile.SourceReports[i].RelativePath < profile.SourceReports[j].RelativePath
	})
}

func buildSourceUserPrompt(source scannedSource) string {
	payload := map[string]any{
		"relative_path": source.RelativePath,
		"sha256":        source.SHA256,
		"size_bytes":    source.SizeBytes,
		"content":       compactSourceContent(source.content),
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	return "Analyze this simulation corpus source and return only the requested JSON object.\n\n" + string(data)
}

func buildMergeUserPrompt(existing *domain.SimulationProfile, reports []domain.SimulationSourceReport) string {
	payload := map[string]any{
		"existing_profile": domain.CompactSimulationProfile(existing),
		"source_reports":   reports,
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	return "Merge these reports into a reusable writing simulation profile. Return only the requested JSON object.\n\n" + string(data)
}

func compactSourceContent(s string) string {
	runes := []rune(s)
	if len(runes) <= maxSourceRunes {
		return s
	}
	head := maxSourceRunes * 3 / 4
	tail := maxSourceRunes - head
	return string(runes[:head]) + "\n\n[...truncated...]\n\n" + string(runes[len(runes)-tail:])
}
