package inboundcoord

import "strings"

// ContinuationContent preserves the trusted utterance while narrowing this
// execution to its planned deliverable. One utterance can authorize multiple
// separate items, so its full text is evidence, not this task's whole scope.
func ContinuationContent(item WindowItem) string {
	var b strings.Builder
	b.WriteString("本次续接只推进这一个交付物：")
	b.WriteString(DisplayMatterTitle(item.Purpose))
	b.WriteString("\n原始发言中其它已分派工作不属于本次执行范围，不要重复执行。")
	if item.LookInto != "" {
		b.WriteString("\n本项必要上下文：")
		b.WriteString(item.LookInto)
	}
	b.WriteString("\n\n原始发言（说话人与原文按来源保留）：\n")
	b.WriteString(item.Content)
	return b.String()
}
