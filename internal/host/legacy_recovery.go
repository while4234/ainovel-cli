package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type LegacyRecoveryField struct {
	Field      string `json:"field"`
	Value      string `json:"value,omitempty"`
	Provenance string `json:"provenance"`
	Confidence string `json:"confidence"`
}

type LegacyRecoveryPreview struct {
	Version                  int                             `json:"version"`
	ID                       string                          `json:"id"`
	Mode                     domain.CoreCastMode             `json:"mode"`
	FoundationRevision       int64                           `json:"foundation_revision"`
	FoundationAuditSignature string                          `json:"foundation_audit_signature"`
	Candidate                domain.CoreCastContract         `json:"candidate"`
	Completion               domain.CoreCastCompletionResult `json:"completion"`
	Recovered                []LegacyRecoveryField           `json:"recovered"`
	Conflicts                []string                        `json:"conflicts"`
	Impact                   []string                        `json:"impact"`
	CreatedAt                string                          `json:"created_at"`
}

// PreviewLegacyRecovery deterministically projects an old StoryFoundation
// into the current CoreCast contract. It never writes or invents prose.
func PreviewLegacyRecovery(st *storepkg.Store) (LegacyRecoveryPreview, error) {
	if st == nil {
		return LegacyRecoveryPreview{}, fmt.Errorf("项目存储不可用")
	}
	started := NewFoundationRevisionService(st).bodyFilesStarted()
	if !started {
		return LegacyRecoveryPreview{}, fmt.Errorf("仅已开始正文且缺少新版绑定的旧项目需要执行恢复")
	}
	if binding, err := st.CoreCast.LoadGateBinding(); err != nil {
		return LegacyRecoveryPreview{}, err
	} else if binding != nil {
		return LegacyRecoveryPreview{}, fmt.Errorf("当前项目已经存在 CoreCast 绑定，无需旧项目恢复")
	}
	foundation, err := st.Foundation.Load()
	if err != nil {
		return LegacyRecoveryPreview{}, fmt.Errorf("读取旧项目设定失败：%w", err)
	}
	audit, err := domain.FoundationAuditSignature(foundation)
	if err != nil {
		return LegacyRecoveryPreview{}, fmt.Errorf("计算设定签名失败：%w", err)
	}
	mode := domain.CoreCastModeNormal
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		return LegacyRecoveryPreview{}, err
	}
	if manifest != nil {
		mode = domain.CoreCastModeAdaptation
	}
	draftHash := domain.ContentSignature([]byte(fmt.Sprintf("legacy-recovery:v1:%s:%d", audit, foundation.Revision)))
	candidate := domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: mode, DraftRevision: 1, DraftHash: draftHash,
		PlannedRelationships: append([]domain.CharacterRelationship(nil), foundation.Relationships...),
	}
	recovered := []LegacyRecoveryField{{Field: "premise", Value: foundation.Premise, Provenance: "StoryFoundation.premise", Confidence: "exact"}}
	for _, character := range foundation.Characters {
		if !legacyCoreCharacter(character) {
			continue
		}
		importance := domain.CoreCastImportanceMajorSupport
		if legacyProtagonist(character) {
			importance = domain.CoreCastImportanceProtagonist
		}
		member := domain.CoreCastMember{
			Character: character, Importance: importance, Origin: domain.CoreCastOriginOriginal,
			MainlineFunction: strings.TrimSpace(character.Role), NoCoreRelationships: len(foundation.Relationships) == 0,
		}
		if mode == domain.CoreCastModeAdaptation {
			member.InclusionRationale = "来自旧项目已确认的目标 StoryFoundation"
		}
		candidate.Members = append(candidate.Members, member)
		recovered = append(recovered, LegacyRecoveryField{Field: "character." + character.ID, Value: character.Name, Provenance: "StoryFoundation.characters", Confidence: "exact"})
	}
	var sourceCharacters, sourceMajor []domain.SourceMajorCharacter
	var sourceMissing []domain.CoreCastMissingItem
	conflicts := []string{}
	if mode == domain.CoreCastModeAdaptation {
		sourceFoundation, loadErr := st.Adaptation.LoadSourceFoundation()
		dossier, dossierErr := st.Adaptation.LoadCoCreateDossier()
		intent, intentErr := st.Adaptation.LoadCoCreateIntent()
		if loadErr != nil || dossierErr != nil || intentErr != nil || sourceFoundation == nil || dossier == nil || intent == nil {
			conflicts = append(conflicts, "源分析、Dossier 或改编意图不完整；请先补全缺失分析")
		} else {
			candidate.SourceSignature = storepkg.AdaptationSourceSignature(*manifest)
			candidate.AdaptationIntentHash = adapt.CoCreateIntentHash(*intent)
			sourceCharacters = domain.ResolveSourceCharacters(*sourceFoundation)
			sourceMajor, sourceMissing = domain.ResolveSourceMajorCharacters(*sourceFoundation, *dossier)
			for _, source := range sourceMajor {
				targetIDs := legacyMatchingTargetIDs(source, candidate.Members)
				action := domain.SourceDispositionExclude
				rationale := "旧目标设定未包含该源作主要角色"
				if len(targetIDs) > 0 {
					action = domain.SourceDispositionKeep
					rationale = "按旧目标设定中的稳定名称或别名匹配"
				}
				candidate.SourceDispositions = append(candidate.SourceDispositions, domain.SourceCharacterDisposition{
					SourceCharacterID: source.ID, Action: action, TargetCharacterIDs: targetIDs, Rationale: rationale,
				})
			}
		}
	}
	normalized, err := domain.NormalizeCoreCastContract(candidate)
	if err != nil {
		return LegacyRecoveryPreview{}, fmt.Errorf("旧设定无法转换为 CoreCast：%w", err)
	}
	completion := domain.CoreCastCompletion(normalized, sourceCharacters, sourceMajor)
	if len(sourceMissing) > 0 {
		completion.Missing = append(completion.Missing, sourceMissing...)
		completion.Complete = false
		for _, item := range sourceMissing {
			completion.BlockingReasons = append(completion.BlockingReasons, item.Description)
		}
	}
	if !completion.Complete {
		conflicts = append(conflicts, completion.BlockingReasons...)
	}
	id := domain.ContentSignature([]byte(fmt.Sprintf("%s:%d:%s", audit, foundation.Revision, normalized.ContentSignature)))
	return LegacyRecoveryPreview{
		Version: 1, ID: id, Mode: mode, FoundationRevision: foundation.Revision, FoundationAuditSignature: audit,
		Candidate: normalized, Completion: completion, Recovered: recovered, Conflicts: uniqueLegacyStrings(conflicts),
		Impact:    []string{"创建旧项目恢复快照", "写入并显式确认 CoreCast", "将确认后的核心角色发布回 StoryFoundation", "不会改写任何正文、草稿或章节"},
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func ApplyLegacyRecovery(st *storepkg.Store, expectedID string, expectedFoundationRevision int64, expectedAudit string) (LegacyRecoveryPreview, error) {
	if receipt, err := loadLegacyRecoveryReceipt(st.Dir(), expectedID); err != nil {
		return LegacyRecoveryPreview{}, err
	} else if receipt != nil {
		if receipt.FoundationRevision != expectedFoundationRevision || receipt.FoundationAuditSignature != strings.TrimSpace(expectedAudit) {
			return LegacyRecoveryPreview{}, fmt.Errorf("恢复请求与已应用回执不一致")
		}
		return *receipt, nil
	}
	preview, err := PreviewLegacyRecovery(st)
	if err != nil {
		return LegacyRecoveryPreview{}, err
	}
	if strings.TrimSpace(expectedID) != preview.ID || expectedFoundationRevision != preview.FoundationRevision || strings.TrimSpace(expectedAudit) != preview.FoundationAuditSignature {
		return LegacyRecoveryPreview{}, fmt.Errorf("恢复预览已过期：项目设定或签名发生变化，请重新预览")
	}
	if !preview.Completion.Complete || len(preview.Conflicts) > 0 {
		return LegacyRecoveryPreview{}, fmt.Errorf("存在阻断性冲突，不能应用恢复：%s", strings.Join(preview.Conflicts, "；"))
	}
	snapshot, err := snapshotLegacyRecoveryFiles(st.Dir(), preview.ID)
	if err != nil {
		return LegacyRecoveryPreview{}, fmt.Errorf("创建恢复快照失败：%w", err)
	}
	rollback := func(cause error) (LegacyRecoveryPreview, error) {
		if restoreErr := restoreLegacyRecoveryFiles(st.Dir(), snapshot); restoreErr != nil {
			return LegacyRecoveryPreview{}, fmt.Errorf("应用恢复失败且回滚失败：%v；回滚错误：%w", cause, restoreErr)
		}
		return LegacyRecoveryPreview{}, cause
	}
	binding, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{
		Mode: preview.Mode, DraftRevision: preview.Candidate.DraftRevision, DraftHash: preview.Candidate.DraftHash,
		SourceSignature: preview.Candidate.SourceSignature, AdaptationIntentHash: preview.Candidate.AdaptationIntentHash,
	})
	if err != nil {
		return rollback(fmt.Errorf("写入 CoreCast 绑定失败：%w", err))
	}
	saved, err := st.CoreCast.SaveCAS(preview.Candidate, 0)
	if err != nil {
		return rollback(fmt.Errorf("写入 CoreCast 候选失败：%w", err))
	}
	sourceCharacters, sourceMajor, sourceMissing, err := legacyRecoverySourceDependencies(st, preview.Mode)
	if err != nil {
		return rollback(err)
	}
	confirmed, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, sourceCharacters, sourceMajor, sourceMissing)
	if err != nil {
		return rollback(fmt.Errorf("确认 CoreCast 失败：%w", err))
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, sourceCharacters, sourceMajor, sourceMissing); err != nil {
		return rollback(fmt.Errorf("发布 CoreCast 到 StoryFoundation 失败：%w", err))
	}
	if _, err := st.CoreCast.RequireConfirmedGate(binding, sourceCharacters, sourceMajor, sourceMissing); err != nil {
		return rollback(fmt.Errorf("恢复后绑定校验失败：%w", err))
	}
	preview.Candidate = confirmed
	if current, loadErr := st.CoreCast.Load(); loadErr == nil && current != nil {
		preview.Candidate = *current
	}
	if err := saveLegacyRecoveryReceipt(st.Dir(), preview); err != nil {
		return rollback(fmt.Errorf("保存恢复回执失败：%w", err))
	}
	return preview, nil
}

func loadLegacyRecoveryReceipt(outputDir, id string) (*LegacyRecoveryPreview, error) {
	if strings.TrimSpace(id) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(outputDir, "meta", "recovery", "receipts", id+".json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt LegacyRecoveryPreview
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, fmt.Errorf("读取恢复回执失败：%w", err)
	}
	if receipt.ID != id {
		return nil, fmt.Errorf("恢复回执签名不匹配")
	}
	return &receipt, nil
}

func saveLegacyRecoveryReceipt(outputDir string, preview LegacyRecoveryPreview) error {
	directory := filepath.Join(outputDir, "meta", "recovery", "receipts")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(preview, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".receipt-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(directory, preview.ID+".json"))
}

func legacyRecoverySourceDependencies(st *storepkg.Store, mode domain.CoreCastMode) ([]domain.SourceMajorCharacter, []domain.SourceMajorCharacter, []domain.CoreCastMissingItem, error) {
	if mode != domain.CoreCastModeAdaptation {
		return nil, nil, nil, nil
	}
	source, err := st.Adaptation.LoadSourceFoundation()
	if err != nil || source == nil {
		return nil, nil, nil, fmt.Errorf("读取 SourceFoundation 失败：%w", err)
	}
	dossier, err := st.Adaptation.LoadCoCreateDossier()
	if err != nil || dossier == nil {
		return nil, nil, nil, fmt.Errorf("读取改编 Dossier 失败：%w", err)
	}
	major, missing := domain.ResolveSourceMajorCharacters(*source, *dossier)
	return domain.ResolveSourceCharacters(*source), major, missing, nil
}

func legacyCoreCharacter(character domain.Character) bool {
	tier := strings.ToLower(strings.TrimSpace(character.Tier))
	return tier == "core" || tier == "important" || legacyProtagonist(character)
}

func legacyProtagonist(character domain.Character) bool {
	role := strings.ToLower(strings.TrimSpace(character.Role))
	return strings.Contains(role, "主角") || strings.Contains(role, "protagonist") || strings.Contains(role, "lead")
}

func legacyMatchingTargetIDs(source domain.SourceMajorCharacter, members []domain.CoreCastMember) []string {
	labels := append([]string{source.Name}, source.Aliases...)
	for _, member := range members {
		for _, target := range append([]string{member.Character.Name}, member.Character.Aliases...) {
			for _, label := range labels {
				if strings.EqualFold(strings.TrimSpace(target), strings.TrimSpace(label)) && strings.TrimSpace(label) != "" {
					return []string{member.Character.ID}
				}
			}
		}
	}
	return nil
}

type legacyRecoverySnapshot struct {
	Root  string
	Files map[string]bool
}

var legacyRecoveryMutableFiles = []string{
	"meta/cocreate/core_cast_contract.json", "meta/cocreate/core_cast_gate.json", "story_foundation.json", "premise.md",
	"characters.json", "characters.md", "world_rules.json", "world_rules.md", "planned_relationships.json",
	"planned_relationships.md", "meta/foundation/projections.json",
}

func snapshotLegacyRecoveryFiles(outputDir, id string) (legacyRecoverySnapshot, error) {
	root := filepath.Join(outputDir, "meta", "recovery", "snapshots", id)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return legacyRecoverySnapshot{}, err
	}
	snapshot := legacyRecoverySnapshot{Root: root, Files: map[string]bool{}}
	for _, relative := range legacyRecoveryMutableFiles {
		source := filepath.Join(outputDir, filepath.FromSlash(relative))
		data, err := os.ReadFile(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return legacyRecoverySnapshot{}, err
		}
		snapshot.Files[relative] = true
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return legacyRecoverySnapshot{}, err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return legacyRecoverySnapshot{}, err
		}
	}
	manifest, _ := json.MarshalIndent(snapshot.Files, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "snapshot.json"), manifest, 0o600); err != nil {
		return legacyRecoverySnapshot{}, err
	}
	return snapshot, nil
}

func restoreLegacyRecoveryFiles(outputDir string, snapshot legacyRecoverySnapshot) error {
	for _, relative := range legacyRecoveryMutableFiles {
		target := filepath.Join(outputDir, filepath.FromSlash(relative))
		if !snapshot.Files[relative] {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		data, err := os.ReadFile(filepath.Join(snapshot.Root, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func uniqueLegacyStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
