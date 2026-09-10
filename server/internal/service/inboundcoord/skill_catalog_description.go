package inboundcoord

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/skill"
)

const coordinatorSkillHeaderBudget = 4096

var managedSkillDescription = regexp.MustCompile(`^Managed by dingtalk-agent( \([0-9A-Fa-f]{6,64}\))?$`)

// A declared frontmatter description repairs only absent/managed metadata.
// It is a capability label, never executable instructions or authorization.
func coordinatorSkillDescription(metadata, contentHead string) string {
	metadata = strings.TrimSpace(metadata)
	if metadata != "" && !managedSkillDescription.MatchString(metadata) {
		return metadata
	}
	header := completeCoordinatorSkillHeader(clipRunes(contentHead, coordinatorSkillHeaderBudget))
	if header == "" {
		return metadata
	}
	_, description := skill.ParseSkillFrontmatter(header)
	if description == "" {
		return metadata
	}
	return clipRunes(collapseSpaces(description), skillSnapshotDescBudget)
}

// Require a complete closing fence inside the bounded DB prefix. A delimiter
// cut at the limit is ambiguous; body text and later separators cannot repair it.
func completeCoordinatorSkillHeader(head string) string {
	if !strings.HasPrefix(head, "---\n") && !strings.HasPrefix(head, "---\r\n") {
		return ""
	}
	offset := strings.IndexByte(head, '\n') + 1
	for offset < len(head) {
		rest := head[offset:]
		relEnd := strings.IndexByte(rest, '\n')
		lineEnd, terminated := len(head), relEnd >= 0
		if terminated {
			lineEnd = offset + relEnd
		}
		line := strings.TrimRight(head[offset:lineEnd], "\r\t ")
		if line == "---" {
			if !terminated && utf8.RuneCountInString(head) == coordinatorSkillHeaderBudget {
				return ""
			}
			return head[:lineEnd]
		}
		if strings.HasPrefix(line, "---") {
			return ""
		}
		if !terminated {
			break
		}
		offset = lineEnd + 1
	}
	return ""
}
