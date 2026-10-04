package employeeverification

import (
	"regexp"
	"strconv"
	"strings"
)

// Deterministic derivation of required checks from explicit done criteria.
// The pattern table is deliberately small: a missed check is acceptable, a
// wrongly invented one is not. Free text only derives checks when the
// requester's own words carry an explicit done-criteria cue (Chinese or
// English). Design reference: GawkBot internal/team/task_dod_derive.go at
// 71e82a1809565281cbd0bf8185d3c125b715d934 (re-implemented, see SOURCE_MAP.md).

const maxDerivedChecks = 8

// File tokens: Han characters are allowed only inside explicit quotes, since
// an unquoted Chinese run cannot be separated from the surrounding sentence.
const (
	fileQuoted   = "[「『“\"'`《]([^」』”\"'`》\\s\\\\]{1,120}\\.[A-Za-z][A-Za-z0-9]{0,7})[」』”\"'`》]"
	fileUnquoted = "((?:[A-Za-z0-9_][A-Za-z0-9_.\\-]*/)*[A-Za-z0-9_][A-Za-z0-9_.\\-]{0,120}\\.[A-Za-z][A-Za-z0-9]{0,7})"
	fileToken    = "(?:" + fileQuoted + "|" + fileUnquoted + ")"
	quotedText   = "[「『“\"'‘`]([^」』”\"'’`\\n]{1,200})[」』”\"'’`]"
	modalZH      = "(?:要|必须|需要|应该|应|得)?"
	numberToken  = "(-?\\d{1,15}(?:\\.\\d{1,9})?)"
)

var (
	cueZH = `完成标准|验收标准|交付标准|完成条件|验收条件|验收要求|(?:做|干)完(?:的)?标准|才(?:能)?算(?:是)?(?:做完|完成|搞定|交付|做好|通过|完事)|(?:别|不要|不许|不准|不能|勿)\s*(?:跟我|给我|向我|和我|对我)?\s*(?:说|讲|报|汇报|回复|告诉我)[^。！？!?\n]{0,8}(?:做完|完成|搞定|好了|结束|ok|OK|done)`
	cueEN = `(?i:\b(?:definition\s+of\s+done|dod|acceptance\s+criteria|done\s+criteria)\b|\b(?:don'?t|do\s+not)\s+(?:tell\s+me|say|report)\s+(?:it'?s|it\s+is|this\s+is|you'?re|you\s+are|that\s+it'?s)\s+(?:done|finished|complete)\s+unless\b)`

	cuePattern   = regexp.MustCompile(cueZH + `|` + cueEN)
	commandAfter = regexp.MustCompile("(?s)(?:" + cueZH + "|" + cueEN + ")[^`]{0,160}`([\\x20-\\x5f\\x61-\\x7e]{2,200})`")
	fileAnywhere = regexp.MustCompile(fileToken)
	containsZH   = regexp.MustCompile(fileToken + `\s*(?:文件)?(?:里面|里|中|内)?\s*` + modalZH + `\s*(?:包含|含有|包括|出现|写有)\s*(?:文字|内容|字样|文本)?\s*` + quotedText)
	containsEN   = regexp.MustCompile(`(?i:\bfile)\s+` + fileToken + `\s+(?i:(?:exists\s+and\s+)?contains\s+(?:the\s+text\s+)?)` + quotedText)
	rowsZH       = regexp.MustCompile(fileToken + `\s*(?:文件)?(?:里面|里|中|内)?\s*` + modalZH + `\s*(?:有|共|包含|含|是|为)?\s*(\d{1,7})\s*(?:行|条)\s*(?:数据|记录)`)
	rowsEN       = regexp.MustCompile(fileToken + `\s+(?i:(?:has|contains|with|should\s+have|must\s+have)\s+(?:exactly\s+)?)(\d{1,7})\s+(?i:data\s+rows|rows\s+of\s+data|records)\b`)
	columnsZH    = regexp.MustCompile(fileToken + `\s*(?:文件)?\s*(?:的)?\s*(?:列名|列|字段|表头)\s*` + modalZH + `\s*(?:为|是|包括|包含|有)\s*[:：]?\s*([^\n。；;！!？?，]{1,200})`)
	columnsEN    = regexp.MustCompile(fileToken + `\s+(?i:(?:has|with|should\s+have|must\s+have)\s+(?:the\s+)?(?:columns|headers))\s*[:=]?\s*([^\n.;]{1,200})`)
	existsZH     = regexp.MustCompile(`(?:文件|附件)\s*` + fileToken + `\s*` + modalZH + `\s*(?:存在|已生成|生成了|生成)`)
	producesZH   = regexp.MustCompile(`(?:生成|产出|导出|输出|交付|写出|保存为|保存成|写入|写到)\s*(?:一份|一个|一张)?\s*(?:文件)?\s*` + fileToken)
	existsEN     = regexp.MustCompile(`(?i:\bfile)\s+` + fileToken + `\s+(?i:exists)\b`)
	valueZH      = regexp.MustCompile(fileToken + `\s*(?:文件)?(?:里面|里|中|内)?\s*(?:的)?\s*(?:内容|结果|值|数值|答案|数字)?\s*(?:应该|必须|要|应)?\s*(?:是|为|等于)\s*(?:` + quotedText + `|` + numberToken + `(\s*[行条列个项])?)`)
	valueAloneZH = regexp.MustCompile(`(?:结果|答案|总和|合计|总数|总计)\s*(?:应该|必须|要|应)?\s*(?:是|为|等于)\s*` + numberToken + `(\s*[行条列个项])?`)
	valueEN      = regexp.MustCompile(fileToken + `\s+(?i:should\s+equal|must\s+equal|equals|content\s+(?:is|should\s+be|must\s+be))\s+(?:` + quotedText + `|` + numberToken + `)`)
	columnSplit  = regexp.MustCompile(`\s*(?:[、,/|]|以及|和|及|\band\b)\s*`)
	columnAbort  = regexp.MustCompile(`等$|行|条|应|要|必须|并且|然后|\d+\s*(?:rows|records)`)
	columnQuotes = "「」『』“”\"'‘’`《》"
)

// HasDoneCue reports whether text carries an explicit done-criteria cue.
func HasDoneCue(text string) bool { return cuePattern.MatchString(normalizeWidth(text)) }

// normalizeWidth folds full-width ASCII letters, digits, quotes and the
// ideographic space to ASCII. Chinese clause punctuation (，。；) is kept so it
// still terminates a clause.
func normalizeWidth(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\u3000':
			return ' '
		case r >= '０' && r <= '９', r >= 'Ａ' && r <= 'Ｚ', r >= 'ａ' && r <= 'ｚ', r == '＂', r == '＇', r == '．', r == '＿', r == '－', r == '／':
			return r - 0xFEE0
		}
		return r
	}, text)
}

// DeriveFromHumanText derives required checks from the requester's own words
// (the outer message text, never quoted or forwarded content). It returns nil
// unless an explicit done-criteria cue is present. The caller stores the
// result as OriginHumanCue with AuthorRef = the Task requester.
func DeriveFromHumanText(text string) []Check {
	text = normalizeWidth(text)
	if !cuePattern.MatchString(text) {
		return nil
	}
	checks := deriveChecks(text)
	if m := commandAfter.FindStringSubmatch(text); m != nil {
		command := strings.TrimSpace(m[1])
		if strings.Contains(command, " ") && fileAnywhere.FindString(command) != command {
			checks = append(checks, Check{Kind: KindExecutionOutput, Command: command})
		}
	}
	return finishDerived(checks)
}

// DeriveFromModelCriteria parses model-written success criteria. They are
// check statements by construction, so no cue is required, but the result
// must be stored as OriginModelProposed and stays inactive until the
// requester confirms it. Commands are never derived from model text.
func DeriveFromModelCriteria(criteria []string) []Check {
	var checks []Check
	for _, criterion := range criteria {
		checks = append(checks, deriveChecks(normalizeWidth(criterion))...)
	}
	return finishDerived(checks)
}

// match is one regexp match with its groups and the byte span of the file token.
type match struct {
	groups    []string
	fileStart int
	fileEnd   int
}

// matches returns submatches whose file token (groups 1/2) is a whole,
// safe name: an unquoted name glued to a preceding path or dot (as in
// "../secret.csv") is rejected rather than shortened.
func matches(p *regexp.Regexp, text string) []match {
	var out []match
	for _, idx := range p.FindAllStringSubmatchIndex(text, -1) {
		m := match{groups: make([]string, len(idx)/2)}
		for g := range m.groups {
			if idx[2*g] >= 0 {
				m.groups[g] = text[idx[2*g]:idx[2*g+1]]
			}
		}
		switch {
		case idx[2] >= 0:
			m.fileStart, m.fileEnd = idx[2], idx[3]
		case idx[4] >= 0:
			m.fileStart, m.fileEnd = idx[4], idx[5]
			if m.fileStart > 0 && strings.ContainsAny(text[m.fileStart-1:m.fileStart], "./\\~") {
				continue
			}
		default:
			continue
		}
		out = append(out, m)
	}
	return out
}

func (m match) file() string {
	name := m.groups[1]
	if name == "" {
		name = m.groups[2]
	}
	safe, ok := safeFileName(name)
	if !ok {
		return ""
	}
	return safe
}

// alternativeAfter reports "a.txt 或 b.txt" style choices, which are not a
// definite deliverable.
var alternativeAfter = regexp.MustCompile(`^[」』”"'\x60》]?\s*(?:或者|或是|或|还是|/|(?i:or)\b)`)

func deriveChecks(text string) []Check {
	var checks []Check
	for _, p := range []*regexp.Regexp{containsZH, containsEN} {
		for _, m := range matches(p, text) {
			if file := m.file(); file != "" {
				checks = append(checks, Check{Kind: KindArtifactContents, File: file, Contains: []string{m.groups[3]}})
			}
		}
	}
	for _, p := range []*regexp.Regexp{rowsZH, rowsEN} {
		for _, m := range matches(p, text) {
			file := m.file()
			rows, err := strconv.Atoi(m.groups[3])
			if file != "" && err == nil && tabularFile(file) {
				checks = append(checks, Check{Kind: KindArtifactContents, File: file, DataRows: &rows})
			}
		}
	}
	for _, p := range []*regexp.Regexp{columnsZH, columnsEN} {
		for _, m := range matches(p, text) {
			file := m.file()
			if columns := splitColumns(m.groups[3]); file != "" && tabularFile(file) && len(columns) > 0 {
				checks = append(checks, Check{Kind: KindArtifactContents, File: file, Columns: columns})
			}
		}
	}
	for _, p := range []*regexp.Regexp{valueZH, valueEN} {
		for _, m := range matches(p, text) {
			file := m.file()
			value := m.groups[3]
			if value == "" {
				value = m.groups[4]
			}
			trailing := ""
			if len(m.groups) > 5 {
				trailing = m.groups[5]
			}
			if file != "" && value != "" && strings.TrimSpace(trailing) == "" {
				checks = append(checks, Check{Kind: KindExecutionOutput, File: file, Expect: value})
			}
		}
	}
	if m := valueAloneZH.FindStringSubmatch(text); m != nil && strings.TrimSpace(m[2]) == "" {
		// A bare "结果应为 5050" binds only when exactly one file is named, so
		// the value is compared with produced output, never with chat text.
		if file := soleFile(text); file != "" {
			checks = append(checks, Check{Kind: KindExecutionOutput, File: file, Expect: m[1]})
		}
	}
	for _, p := range []*regexp.Regexp{existsZH, producesZH, existsEN} {
		for _, m := range matches(p, text) {
			if file := m.file(); file != "" && !alternativeAfter.MatchString(text[m.fileEnd:]) {
				checks = append(checks, Check{Kind: KindArtifactContents, File: file})
			}
		}
	}
	return checks
}

func soleFile(text string) string {
	seen := map[string]bool{}
	for _, m := range matches(fileAnywhere, text) {
		if file := m.file(); file != "" {
			seen[file] = true
		}
	}
	if len(seen) != 1 {
		return ""
	}
	for file := range seen {
		return file
	}
	return ""
}

func splitColumns(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range columnSplit.Split(raw, -1) {
		part = strings.Trim(strings.TrimSpace(part), columnQuotes)
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if columnAbort.MatchString(part) || len([]rune(part)) > 32 {
			return nil
		}
		out = append(out, part)
	}
	if len(out) > maxColumns {
		return nil
	}
	return out
}

// finishDerived drops bare existence checks made redundant by a stronger
// check on the same file, validates each check and caps the count.
func finishDerived(checks []Check) []Check {
	strong := map[string]bool{}
	for _, c := range checks {
		if c.File != "" && (len(c.Contains) > 0 || c.DataRows != nil || len(c.Columns) > 0 || c.Expect != "") {
			strong[c.File] = true
		}
	}
	seen := map[string]bool{}
	var out []Check
	for _, c := range checks {
		if c.Kind == KindArtifactContents && len(c.Contains) == 0 && c.DataRows == nil && len(c.Columns) == 0 && strong[c.File] {
			continue
		}
		normalized, err := normalizeCheck(c)
		if err != nil || seen[normalized.ID] {
			continue
		}
		seen[normalized.ID] = true
		out = append(out, normalized)
		if len(out) == maxDerivedChecks {
			break
		}
	}
	return out
}
