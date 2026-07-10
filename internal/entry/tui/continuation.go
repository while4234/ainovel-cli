package tui

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func parseContinuationCommand(args []string) (string, string, error) {
	if len(args) == 0 {
		return "status", "", nil
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	if action == "show" {
		action = "status"
	}
	switch action {
	case "status", "generate", "approve", "retry", "start":
		if len(args) > 1 {
			return "", "", fmt.Errorf("/continuation %s 不接受额外参数", action)
		}
		return action, "", nil
	case "revise":
		instruction := strings.TrimSpace(strings.Join(args[1:], " "))
		if instruction == "" {
			return "", "", fmt.Errorf("用法：/continuation revise <修改要求>")
		}
		return action, instruction, nil
	default:
		return "", "", fmt.Errorf("未知续写操作 %q；可用 status、generate、approve、revise、retry、start", action)
	}
}

func continuationActionSummary(action string, snapshot *domain.ContinuationSnapshot, label string) string {
	if action == "start" && strings.TrimSpace(label) != "" {
		return "续写已开始：" + label
	}
	if snapshot == nil {
		return "续写规划已更新"
	}
	return fmt.Sprintf("续写规划：%s（revision %d）", snapshot.Workflow.Stage, snapshot.Workflow.Revision)
}

func formatContinuationSnapshot(snapshot *domain.ContinuationSnapshot) string {
	if snapshot == nil {
		return ""
	}
	lines := []string{
		fmt.Sprintf("阶段：%s", snapshot.Workflow.Stage),
		fmt.Sprintf("原作基线：1-%d 章", snapshot.Workflow.BaseChapterCount),
		fmt.Sprintf("Revision：%d", snapshot.Workflow.Revision),
	}
	if draft := strings.TrimSpace(snapshot.Workflow.Draft); draft != "" {
		lines = append(lines, "Draft："+draft)
	}
	if snapshot.Proposal != nil {
		lines = append(lines,
			fmt.Sprintf("提案：%s（%s，计划 %d 章）", snapshot.Proposal.Summary, snapshot.Proposal.Structure, snapshot.Proposal.TargetChapterCount),
			"方向："+snapshot.Proposal.Direction,
		)
	}
	for _, volume := range snapshot.Volumes {
		lines = append(lines, fmt.Sprintf("第%d卷 %s：%s", volume.Index, volume.Title, volume.Theme))
	}
	if snapshot.Outlines != nil {
		chapters, err := domain.FlattenContinuationOutline(snapshot.Workflow.BaseChapterCount, *snapshot.Outlines)
		if err == nil {
			for _, chapter := range chapters {
				lines = append(lines, fmt.Sprintf("第%d章 %s：%s", chapter.Chapter, chapter.Title, chapter.CoreEvent))
			}
		}
	}
	return strings.Join(lines, "\n")
}
