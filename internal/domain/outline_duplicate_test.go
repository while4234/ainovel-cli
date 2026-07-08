package domain

import "testing"

func TestFindDuplicateOutlineEntries(t *testing.T) {
	duplicate, ok := FindDuplicateOutlineEntries([]OutlineEntry{
		{Chapter: 1, Title: "鹰符潜入与幻象识破", CoreEvent: "良逸发现妖风为幻象，找到地下祭台入口。", Hook: "他看见青小竹被困。"},
		{Chapter: 2, Title: "地宫之战：围杀尹浩", CoreEvent: "三人合力斩断阵旗。", Hook: "尹浩吐出黑雾。"},
		{Chapter: 3, Title: " 鹰符潜入 与 幻象识破 ", CoreEvent: "良逸发现妖风为幻象找到地下祭台入口", Hook: "他看见青小竹被困"},
	})
	if !ok {
		t.Fatal("expected duplicate outline")
	}
	if duplicate.Chapter != 3 || duplicate.ExistingChapter != 1 {
		t.Fatalf("duplicate = %+v, want chapter 3 repeating chapter 1", duplicate)
	}
}

func TestFindDuplicateOutlineEntriesUsesPreviousGroups(t *testing.T) {
	previous := []OutlineEntry{
		{Chapter: 4, Title: "黑风审讯", CoreEvent: "良逸逼问出密道钥匙。", Hook: "钥匙裂出血纹。"},
	}
	duplicate, ok := FindDuplicateOutlineEntries([]OutlineEntry{
		{Chapter: 8, Title: "黑风审讯", CoreEvent: "良逸逼问出密道钥匙。", Hook: "钥匙裂出血纹。"},
	}, previous)
	if !ok {
		t.Fatal("expected duplicate from previous group")
	}
	if duplicate.Chapter != 8 || duplicate.ExistingChapter != 4 {
		t.Fatalf("duplicate = %+v, want chapter 8 repeating chapter 4", duplicate)
	}
}

func TestFindDuplicateOutlineEntriesIgnoresIncompleteSignatures(t *testing.T) {
	if duplicate, ok := FindDuplicateOutlineEntries([]OutlineEntry{
		{Chapter: 1, Title: "同题", CoreEvent: "", Hook: "同钩子"},
		{Chapter: 2, Title: "同题", CoreEvent: "", Hook: "同钩子"},
	}); ok {
		t.Fatalf("incomplete signatures should not duplicate, got %+v", duplicate)
	}
}
