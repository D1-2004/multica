package assoc

import (
	"strings"
	"testing"
)

func TestTechnicalSubjectsArePreservedWithoutLexicalVeto(t *testing.T) {
	for _, subject := range []string{"dws chat", "dws todo", "dws mail", "data-auth", "openConversationId", "kubectl", "在钉钉会话中的消息"} {
		purpose := "分析 " + subject + " 的参数含义并核对失败原因"
		got, err := ComposeCoordinatorPurpose("工程师", "", purpose)
		if err != nil || !strings.Contains(got, purpose) {
			t.Fatalf("subject %q lost: %q %v", subject, got, err)
		}
	}
	if _, err := ComposeCoordinatorPurpose("", "", "排查命令参数的问题并给出建议"); err == nil {
		t.Fatal("delegator boundary lost")
	}
}
