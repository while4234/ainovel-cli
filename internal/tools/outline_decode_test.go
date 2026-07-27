package tools

import (
	"strings"
	"testing"
)

func TestDecodeOutlineEntriesAcceptsStructuredScenesWithoutDataLoss(t *testing.T) {
	entries, err := decodeOutlineEntries("expand_arc chapters", `{
		"chapters": [{
			"chapter": 1,
			"title": "董事会暗门",
			"core_event": "林舒然必须在董事会投票前确认内鬼。",
			"hook": "沈辞带回一份被改写的门禁记录。",
			"scenes": [
				"林舒然在车内复盘昨夜的监控。",
				{
					"location": "董事会休息室",
					"characters": ["林舒然", "沈辞"],
					"goal": "确认谁接触过原始记录",
					"conflict": "沈辞不能暴露信息来源",
					"outcome": "两人决定用假名单反向试探",
					"custom_fact": {"evidence": "门禁副本", "risk": 3}
				}
			]
		}]
	}`)
	if err != nil {
		t.Fatalf("decodeOutlineEntries: %v", err)
	}
	if len(entries) != 1 || len(entries[0].Scenes) != 2 {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	structured := entries[0].Scenes[1]
	for _, want := range []string{
		"location: 董事会休息室",
		`characters: ["林舒然","沈辞"]`,
		"goal: 确认谁接触过原始记录",
		"conflict: 沈辞不能暴露信息来源",
		"outcome: 两人决定用假名单反向试探",
		`custom_fact: {"evidence":"门禁副本","risk":3}`,
	} {
		if !strings.Contains(structured, want) {
			t.Fatalf("structured scene %q missing %q", structured, want)
		}
	}
}

func TestDecodeOutlineEntriesAcceptsSingleSceneStringAndArray(t *testing.T) {
	for name, content := range map[string]string{
		"single": `[{"title":"一","core_event":"推进","hook":"悬念","scenes":"完整单场"}]`,
		"array":  `[{"title":"一","core_event":"推进","hook":"悬念","scenes":["场一","场二"]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			entries, err := decodeOutlineEntries("outline", content)
			if err != nil {
				t.Fatalf("decodeOutlineEntries: %v", err)
			}
			if len(entries) != 1 || len(entries[0].Scenes) == 0 {
				t.Fatalf("unexpected entries: %+v", entries)
			}
		})
	}
}
