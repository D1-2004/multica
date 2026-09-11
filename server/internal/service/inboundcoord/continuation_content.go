package inboundcoord

import "strings"

// ContinuationContent preserves the trusted utterance while narrowing this
// execution to its planned deliverable. One utterance can authorize multiple
// separate items, so its full text is evidence, not this task's whole scope.
func ContinuationContent(item WindowItem) string {
	deliverable := strings.TrimSpace(item.LookInto)
	if deliverable == "" {
		deliverable = DisplayMatterTitle(item.Purpose)
	}
	var b strings.Builder
	b.WriteString("本次续接只推进这一个交付物：")
	b.WriteString(deliverable)
	b.WriteString("\n原始发言中其它已分派工作不属于本次执行范围，不要重复执行。")
	purposeTitle := DisplayMatterTitle(item.Purpose)
	if purposeTitle != "" && purposeTitle != deliverable {
		b.WriteString("\n事项简报：")
		b.WriteString(item.Purpose)
	}
	b.WriteString("\n\n原始发言（说话人与原文按来源保留）：\n")
	b.WriteString(item.Content)
	return b.String()
}
