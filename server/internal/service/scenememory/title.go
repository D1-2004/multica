package scenememory

import (
	"regexp"
	"strings"
	"unicode"
)

var memberLine = regexp.MustCompile(`^成员[:：]\s*(.+)$`)

// DisplayTitle is the owner-visible conversation name. Stored scene_title wins
// when it is a real name; otherwise the first locating line (or the member
// list) fills the gap so group rows do not all collapse to "untitled".
func DisplayTitle(stored, memoryText string) string {
	stored = strings.TrimSpace(stored)
	if stored != "" && !genericSceneTitle(stored) {
		return stored
	}
	if locating := LocatingTitle(memoryText); locating != "" {
		return locating
	}
	return stored
}

// LocatingTitle reads a human name out of Host markdown. Generic fillers such
// as "钉钉群聊" are skipped so the member line can still name the scene.
func LocatingTitle(memoryText string) string {
	body := locatingBody(memoryText)
	named := ""
	members := ""
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimSpace(strings.TrimLeft(line, "-*#"))
		if line == "" || placeholderMemoryLine(line) {
			continue
		}
		if match := memberLine.FindStringSubmatch(line); len(match) == 2 {
			members = strings.TrimRightFunc(strings.TrimSpace(match[1]), isTitlePunct)
			continue
		}
		if named == "" && !genericSceneTitle(line) {
			named = strings.TrimRightFunc(line, isTitlePunct)
		}
	}
	if named != "" {
		return named
	}
	return members
}

func locatingBody(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if !strings.Contains(text, "##") {
		return text
	}
	fallback := ""
	for _, part := range strings.Split(text, "##") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		heading := part
		body := ""
		if nl := strings.IndexByte(part, '\n'); nl >= 0 {
			heading = strings.TrimSpace(part[:nl])
			body = strings.TrimSpace(part[nl+1:])
		}
		if fallback == "" {
			fallback = body
			if fallback == "" {
				fallback = heading
			}
		}
		if heading == "场域定位" {
			return body
		}
	}
	return fallback
}

func genericSceneTitle(line string) bool {
	n := strings.TrimRightFunc(strings.TrimSpace(line), isTitlePunct)
	switch n {
	case "钉钉群聊", "钉钉群", "钉钉单聊", "钉钉消息",
		"本会话尚在观察中", "[推断] 本会话尚在观察中":
		return true
	}
	return strings.HasPrefix(n, "[推断]")
}

func placeholderMemoryLine(line string) bool {
	switch strings.TrimSpace(line) {
	case "(暂无)", "（暂无）", "(none)", "暂无", "无":
		return true
	}
	return false
}

func isTitlePunct(r rune) bool {
	return r == '。' || r == '．' || r == '.' || unicode.IsSpace(r)
}
