package imp

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/voocel/ainovel-cli/internal/utils"
)

// 默认章节标题正则。覆盖常见中文（第N章/回/话/卷/节/幕、卷N、序章/楔子/尾声/番外/外传 等）
// 与英文（Chapter N、Prologue、Epilogue）标题，兼容 Markdown 标题前缀（# / ##）、
// 起点系 txt 的「正文 第N章」前缀、以及【】〖〗包裹的标题。
//
// 命名分组：副标题组优先于关键词组（提取时按 priority 顺序回退）：
//   - cn    编号章节副标题（第X章/回/话/卷/节/幕 之后的文字）
//   - vol   独立卷副标题（卷X 之后的文字）
//   - sp    特殊单元副标题（序章/楔子/尾声/番外 之后的文字）
//   - en    英文章节副标题（Chapter X / Prologue / Epilogue 之后的文字）
//   - spkw  特殊单元关键词本身（无副标题时作标题，如「楔子」「番外」）
//   - enkw  英文特殊单元关键词本身（无副标题时作标题，如「Prologue」）

// ws 是字符类内容：ASCII 空白 + 全角空格。Go RE2 的 \s 只含 ASCII 空白，
// 而中文排版的标题分隔常用 U+3000（「第一章　风起」）。
const ws = `\s\x{3000}`

// cnNum 是章节编号可用的数字字符：阿拉伯 / 全角 / 中文小写 / 中文大写繁体（壹贰叁…萬）。
const cnNum = `零〇○Ｏ０一二三四五六七八九十百千万两廿卅壹贰貳叁參肆伍陆陸柒捌玖拾佰仟萬兩\d`

// sub 是副标题捕获：取到行尾，但不吞掉右包裹符（】〗），留给结尾的可选闭括号。
const sub = `[^】〗\n]*`

const specialUnit = `序章|序幕|楔子|引子|前言|序言|尾声|终章|后记|番外|外传`

var defaultChapterRegex = regexp.MustCompile(
	`(?im)^#{0,2}[` + ws + `]*(?:正文[` + ws + `]*)?[【〖]?[` + ws + `]*(?:` +
		`第\s*(?:[` + cnNum + `]+)\s*(?:章|回|话|卷|节|幕)` +
		`(?:[:：．\.` + ws + `]+(?P<cn>` + sub + `))?` +
		`|` +
		`卷\s*(?:[` + cnNum + `]+)` +
		`(?:[:：．\.` + ws + `]+(?P<vol>` + sub + `))?` +
		`|` +
		`(?P<spkw>(?:(?:` + specialUnit + `)(?:[` + cnNum + `]+)?))` +
		`(?:[:：．\.` + ws + `]+(?P<sp>` + sub + `))?` +
		`|` +
		`(?:Chapter\s+(?:\d+|[IVXLCDM]+)|(?P<enkw>Prologue|Epilogue))` +
		`(?:[:：．\.` + ws + `]+(?P<en>` + sub + `))?` +
		`)[` + ws + `]*[】〗]?[` + ws + `]*$`,
)

var volumePrefixRegex = regexp.MustCompile(
	`(?im)^#{0,2}[` + ws + `]*(?:正文[` + ws + `]*)?[【〖]?[` + ws + `]*` +
		`(?:第\s*(?:[` + cnNum + `]+)\s*卷|卷\s*(?:[` + cnNum + `]+))` +
		`[:：．\.` + ws + `]+(?P<tail>` + sub + `)[】〗]?[` + ws + `]*$`,
)

var chapterCueRegex = regexp.MustCompile(
	`(?i)(?:第\s*(?:[` + cnNum + `]+)\s*(?:章|回|话|节|幕)|` +
		`(?:(?:` + specialUnit + `)(?:[` + cnNum + `]+)?))`,
)

var volumeNumberedChapterRegex = regexp.MustCompile(
	`(?i)第\s*(?:[` + cnNum + `]+)\s*(?:章|回|话|节|幕)` +
		`(?:[:：．\.` + ws + `]+(?P<title>` + sub + `))?`,
)

var bareChineseChapterRegex = regexp.MustCompile(
	`(?im)^#{0,2}[` + ws + `]*(?:正文[` + ws + `]*)?[【〖]?[` + ws + `]*` +
		`(?:[十廿卅][一二三四五六七八九]?|[零〇○一二三四五六七八九十百千万两廿卅壹贰貳叁參肆伍陆陸柒捌玖拾佰仟萬兩]{2,})` +
		`\s*章(?:[:：．\.` + ws + `]+(?P<title>` + sub + `))?[】〗]?[` + ws + `]*$`,
)

var nonStoryHeadingRegex = regexp.MustCompile(
	`(?im)^#{0,2}[` + ws + `]*(?:正文[` + ws + `]*)?[【〖]?[` + ws + `]*` +
		`(?:灵异档案(?:及编者语)?|编者语|闲话|.*预告)` +
		`(?:[:：．\.` + ws + `]+` + sub + `)?[】〗]?[` + ws + `]*$`,
)

var inlineTrailingChapterRegex = regexp.MustCompile(
	`(?i)(?P<prefix>.+[。！？!?」”])(?P<title>第\s*(?:[` + cnNum + `]+)\s*(?:章|回|话|节|幕)[` + ws + `]+` + sub + `)$`,
)

// SplitFile 把单个文本文件切分成章节列表。
func SplitFile(path string) ([]Chapter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	text := utils.DecodeText(data)
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("source file is empty: %s", path)
	}
	return splitText(text, defaultChapterRegex), nil
}

type splitMarker struct {
	line          int
	title         string
	chapter       bool
	volumeOnly    bool
	fallbackTitle bool
}

// splitText 是纯函数版切分，便于单测。
func splitText(text string, pattern *regexp.Regexp) []Chapter {
	lines := normalizeChapterLines(strings.Split(text, "\n"))
	var marks []splitMarker
	for i, ln := range lines {
		if mark, ok := parseMarker(ln, pattern, len(marks)+1); ok {
			marks = append(marks, splitMarker{
				line:          i,
				title:         mark.title,
				chapter:       mark.chapter,
				volumeOnly:    mark.volumeOnly,
				fallbackTitle: mark.fallbackTitle,
			})
		}
	}
	if len(marks) == 0 {
		return nil
	}

	for i := range marks {
		if marks[i].volumeOnly && nextMarkerFollowsOnlyBlankLines(lines, marks, i) {
			marks[i].chapter = false
		}
	}

	chapters := make([]Chapter, 0, len(marks))
	for i, m := range marks {
		if !m.chapter {
			continue
		}
		end := len(lines)
		if i+1 < len(marks) {
			end = marks[i+1].line
		}
		body := strings.Join(lines[m.line+1:end], "\n")
		body = stripTrailingNoise(body)
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		title := m.title
		if m.fallbackTitle {
			title = fmt.Sprintf("第%d章", len(chapters)+1)
		}
		chapters = append(chapters, Chapter{Title: title, Content: body})
	}
	return chapters
}

type parsedMarker struct {
	title         string
	chapter       bool
	volumeOnly    bool
	fallbackTitle bool
}

func parseMarker(line string, pattern *regexp.Regexp, fallbackNum int) (parsedMarker, bool) {
	if loc := volumePrefixRegex.FindStringSubmatchIndex(line); loc != nil {
		tail := extractNamedGroup(line, volumePrefixRegex, loc, "tail")
		if tail == "" {
			return parsedMarker{}, false
		}
		if isNonStoryVolumeTail(tail) {
			return parsedMarker{title: strings.TrimSpace(tail), chapter: false}, true
		}
		return parsedMarker{
			title:      extractVolumePrefixedTitle(tail),
			chapter:    true,
			volumeOnly: !hasChapterCue(tail),
		}, true
	}
	if loc := bareChineseChapterRegex.FindStringSubmatchIndex(line); loc != nil {
		title := extractNamedGroup(line, bareChineseChapterRegex, loc, "title")
		fallbackTitle := false
		if title == "" {
			title = fmt.Sprintf("第%d章", fallbackNum)
			fallbackTitle = true
		}
		return parsedMarker{title: title, chapter: true, fallbackTitle: fallbackTitle}, true
	}
	if loc := pattern.FindStringSubmatchIndex(line); loc != nil {
		title := extractTitle(line, pattern, loc, fallbackNum)
		return parsedMarker{
			title:         title,
			chapter:       true,
			volumeOnly:    isVolumeOnlyTitle(line),
			fallbackTitle: title == fmt.Sprintf("第%d章", fallbackNum),
		}, true
	}
	if nonStoryHeadingRegex.MatchString(line) {
		return parsedMarker{title: strings.TrimSpace(line), chapter: false}, true
	}
	return parsedMarker{}, false
}

func nextMarkerFollowsOnlyBlankLines(lines []string, marks []splitMarker, idx int) bool {
	if idx+1 >= len(marks) {
		return false
	}
	for line := marks[idx].line + 1; line < marks[idx+1].line; line++ {
		if strings.TrimSpace(lines[line]) != "" {
			return false
		}
	}
	return true
}

func normalizeChapterLines(lines []string) []string {
	normalized := make([]string, 0, len(lines))
	for _, line := range lines {
		prefix, title, ok := splitInlineTrailingChapter(line)
		if !ok {
			normalized = append(normalized, line)
			continue
		}
		if strings.TrimSpace(prefix) != "" {
			normalized = append(normalized, prefix)
		}
		normalized = append(normalized, title)
	}
	return normalized
}

func splitInlineTrailingChapter(line string) (string, string, bool) {
	loc := inlineTrailingChapterRegex.FindStringSubmatchIndex(line)
	if loc == nil {
		return "", "", false
	}
	prefix := extractNamedGroup(line, inlineTrailingChapterRegex, loc, "prefix")
	title := extractNamedGroup(line, inlineTrailingChapterRegex, loc, "title")
	if prefix == "" || title == "" {
		return "", "", false
	}
	return prefix, title, true
}

func isVolumeOnlyTitle(line string) bool {
	if loc := volumePrefixRegex.FindStringSubmatchIndex(line); loc != nil {
		tail := extractNamedGroup(line, volumePrefixRegex, loc, "tail")
		return tail != "" && !hasChapterCue(tail)
	}
	return false
}

func isNonStoryVolumeTail(tail string) bool {
	tail = strings.TrimSpace(tail)
	return strings.Contains(tail, "预告") ||
		strings.Contains(tail, "灵异档案") ||
		strings.Contains(tail, "编者语")
}

func extractVolumePrefixedTitle(tail string) string {
	tail = strings.TrimSpace(tail)
	if loc := volumeNumberedChapterRegex.FindStringSubmatchIndex(tail); loc != nil {
		if title := extractNamedGroup(tail, volumeNumberedChapterRegex, loc, "title"); title != "" {
			return title
		}
		return strings.TrimSpace(tail[loc[0]:loc[1]])
	}
	if loc := chapterCueRegex.FindStringIndex(tail); loc != nil {
		return strings.TrimSpace(tail[loc[0]:])
	}
	return tail
}

func hasChapterCue(text string) bool {
	return chapterCueRegex.MatchString(text)
}

func extractNamedGroup(line string, pattern *regexp.Regexp, loc []int, name string) string {
	idx := pattern.SubexpIndex(name)
	if idx <= 0 || loc[2*idx] < 0 {
		return ""
	}
	return strings.TrimSpace(line[loc[2*idx]:loc[2*idx+1]])
}

// extractTitle 从匹配行提取章节标题；优先取命名捕获，否则回退章节号占位。
func extractTitle(line string, pattern *regexp.Regexp, loc []int, fallbackNum int) string {
	subnames := pattern.SubexpNames()
	priority := []string{"cn", "vol", "sp", "en", "spkw", "enkw"}
	for _, name := range priority {
		idx := pattern.SubexpIndex(name)
		if idx <= 0 {
			continue
		}
		if loc[2*idx] < 0 {
			continue
		}
		if t := strings.TrimSpace(line[loc[2*idx]:loc[2*idx+1]]); t != "" {
			if name == "sp" && isNumberOnlyTitle(t) {
				continue
			}
			return t
		}
	}
	// 兜底：取第一个非空捕获组（防御性，默认正则的命名组已覆盖各分支）
	for i := 1; i < len(subnames); i++ {
		if loc[2*i] < 0 {
			continue
		}
		if t := strings.TrimSpace(line[loc[2*i]:loc[2*i+1]]); t != "" {
			return t
		}
	}
	return fmt.Sprintf("第%d章", fallbackNum)
}

var numberOnlyTitleRegex = regexp.MustCompile(`^[` + cnNum + `]+$`)

func isNumberOnlyTitle(title string) bool {
	return numberOnlyTitleRegex.MatchString(strings.TrimSpace(title))
}

// stripTrailingNoise 剥离常见的尾部噪声（Project Gutenberg 等 license trailer）。
var trailerRe = regexp.MustCompile(`(?im)^\s*Project Gutenberg(?:\(TM\)|™)?[\s\S]*$`)

func stripTrailingNoise(content string) string {
	if loc := trailerRe.FindStringIndex(content); loc != nil {
		return strings.TrimRight(content[:loc[0]], " \t\n")
	}
	return content
}
