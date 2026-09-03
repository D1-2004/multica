package scenememory

import "testing"

func TestDisplayTitlePrefersStoredName(t *testing.T) {
	got := DisplayTitle("场域回归-R7A", "## 场域定位\n钉钉群聊。\n")
	if got != "场域回归-R7A" {
		t.Fatalf("got %q", got)
	}
}

func TestDisplayTitleSkipsGenericGroupFiller(t *testing.T) {
	got := DisplayTitle("", "## 场域定位\n钉钉群聊。\n成员：SixSix、东翔测试号。\n")
	if got != "SixSix、东翔测试号" {
		t.Fatalf("got %q", got)
	}
}

func TestDisplayTitleUsesLocatingName(t *testing.T) {
	got := DisplayTitle("钉钉群聊", "## 场域定位\n场域回归-R7A。\n成员：冬翔。\n")
	if got != "场域回归-R7A" {
		t.Fatalf("got %q", got)
	}
}

func TestDisplayTitleDMFallsBackToStored(t *testing.T) {
	got := DisplayTitle("冬翔", "")
	if got != "冬翔" {
		t.Fatalf("got %q", got)
	}
}
