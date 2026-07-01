package host

import "testing"

func TestParseSuggestionsStripsListMarkersAndCapsResults(t *testing.T) {
	got := parseSuggestions(`
<uggestions>
- 增强女主线
* 改成双主角
1. 加一条悬疑暗线
2. 这一条超过上限
</suggestions>
`)
	want := []string{"增强女主线", "改成双主角", "加一条悬疑暗线"}
	if len(got) != len(want) {
		t.Fatalf("suggestions length = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestion[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
