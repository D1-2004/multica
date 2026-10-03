package employeememory

import (
	"strings"
	"testing"
)

// A text needs at least two distinct overlapping units: one shared word is
// not recall, and chit-chat never matches a duty topic.
func TestRankTextsMinOverlapTwoChinese(t *testing.T) {
	texts := []string{
		"中午大家吃什么",
		"发版定在周四，记得提前冻结代码",
		"周四有雨",
		"下周发版计划待定",
	}
	hits := RankTexts("发版是哪天？周四吗", texts, 5)
	if len(hits) == 0 || hits[0].Index != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	for _, hit := range hits {
		if hit.Overlap < RetrievalMinOverlap {
			t.Fatalf("hit below min overlap: %+v", hit)
		}
		if hit.Index == 0 || hit.Index == 2 {
			t.Fatalf("single-unit text matched: %+v", hit)
		}
	}
	if got := RankTexts("中午吃什么", []string{"负责周报和发版计划的汇总"}, 5); len(got) != 0 {
		t.Fatalf("chit-chat matched duty: %+v", got)
	}
	if got := TextOverlap("值班核对：本场候选中，漏了哪一项回执？只回编号。", "本场候选编号有 A7、B3、C9。回执：A7 已收到；C9 已收到。"); len(got) < RetrievalMinOverlap {
		t.Fatalf("duty overlap = %v", got)
	}
	if got := TextOverlap("周报哪天交", "负责每周周报的收集与提交"); len(got) != 1 {
		t.Fatalf("one shared word = %v", got)
	}
	if got := RankTexts("ok", texts, 5); got != nil {
		t.Fatalf("unsearchable query matched: %+v", got)
	}
	tied := RankTexts("发版周四", []string{"周四发版", "周四发版"}, 1)
	if len(tied) != 1 || tied[0].Index != 0 || strings.Join(tied[0].Matched, ",") == "" {
		t.Fatalf("tie = %+v", tied)
	}
}
