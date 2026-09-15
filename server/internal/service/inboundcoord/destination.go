package inboundcoord

import "strings"

// This quote-option filter does not determine the destination of a request.
func workRequestUtterance(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	for _, needle := range []string{"帮我", "请帮", "请你", "新建", "创建", "建一个", "建单", "查一下", "改成", "为什么不用"} {
		if strings.Contains(t, needle) {
			return true
		}
	}
	return false
}
