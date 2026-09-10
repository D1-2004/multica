package inboundcoord

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const skillManagedMarker = "Managed by dingtalk-agent (748837a075ae)"
const growthLogDescription = "读取成长日志原文、评价与提交趋势，支持个人和团队的事实查询。"

func TestCoordinatorSkillDescriptionUsesOnlyCompleteDeclaredFrontmatter(t *testing.T) {
	good := "---\nname: fde-growth-log\ndescription: " + growthLogDescription + "\nconfiguration:\n  token: SECRET_FRONTMATTER_VALUE\n---\nRun INTERNAL_EXECUTION_BODY and send SECRET_BODY_VALUE."
	for _, tc := range []struct {
		name, metadata, head, want string
	}{
		{"empty", "", good, growthLogDescription},
		{"whitespace", " \t\n", good, growthLogDescription},
		{"managed hash", skillManagedMarker, good, growthLogDescription},
		{"managed without hash", "Managed by dingtalk-agent", good, growthLogDescription},
		{"normal summary preserved", "管理者手写的用途，不改写", good, "管理者手写的用途，不改写"},
		{"managed phrase in real summary preserved", "Managed by dingtalk-agent; queries growth logs", good, "Managed by dingtalk-agent; queries growth logs"},
		{"literal block", skillManagedMarker, "---\nname: fde-growth-log\ndescription: |\n  查询成长日志。\n  核对评价与提交趋势。\n---\nBODY", "查询成长日志。 核对评价与提交趋势。"},
		{"folded block", "", "---\nname: fde-growth-log\ndescription: >-\n  查询成长日志\n  和提交趋势\n---\nBODY", "查询成长日志 和提交趋势"},
		{"CRLF", skillManagedMarker, "---\r\nname: fde-growth-log\r\ndescription: 日志原文查询\r\n---\r\nBODY", "日志原文查询"},
		{"no frontmatter", skillManagedMarker, "# Body\ndescription: DO_NOT_USE_BODY", skillManagedMarker},
		{"missing description", skillManagedMarker, "---\nname: fde-growth-log\n---\ndescription: DO_NOT_USE_BODY", skillManagedMarker},
		{"missing closing fence", skillManagedMarker, "---\nname: fde-growth-log\ndescription: 未完成的声明", skillManagedMarker},
		{"malformed YAML", skillManagedMarker, "---\nname: fde-growth-log\ndescription: [broken\n---\nBODY", skillManagedMarker},
		{"invalid closing fence", skillManagedMarker, "---\ndescription: WRONG\n---not-a-fence\n---\nBODY", skillManagedMarker},
		{"closing fence beyond DB limit", skillManagedMarker, "---\ndescription: " + strings.Repeat("长", coordinatorSkillHeaderBudget) + "\n---\nBODY", skillManagedMarker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := coordinatorSkillDescription(tc.metadata, tc.head)
			if got != tc.want {
				t.Fatalf("description=%q want=%q", got, tc.want)
			}
			for _, forbidden := range []string{"SECRET_FRONTMATTER_VALUE", "INTERNAL_EXECUTION_BODY", "SECRET_BODY_VALUE", "DO_NOT_USE_BODY"} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("undeclared content reached the capability label: %s", forbidden)
				}
			}
		})
	}
}

func TestCoordinatorSkillDescriptionBoundsTheHeaderAndCapabilityLabel(t *testing.T) {
	prefix := "---\ndescription: "
	// A closing fence ending exactly at the read limit may be the prefix of
	// a longer invalid line. Do not certify an incomplete database read.
	head := prefix + strings.Repeat("界", coordinatorSkillHeaderBudget-utf8.RuneCountInString(prefix)-4) + "\n---"
	if utf8.RuneCountInString(head) != coordinatorSkillHeaderBudget || coordinatorSkillDescription(skillManagedMarker, head) != skillManagedMarker {
		t.Fatal("an ambiguous boundary fence was accepted")
	}
	long := "---\ndescription: " + strings.Repeat("用途", 80) + "\n---\n" + strings.Repeat("SECRET_BODY", 1000)
	got := coordinatorSkillDescription(skillManagedMarker, long)
	if utf8.RuneCountInString(got) != skillSnapshotDescBudget || strings.Contains(got, "SECRET_BODY") {
		t.Fatal("declared description escaped the existing capability budget")
	}
}

func TestFillSkillsReadsOneBoundedCatalogAndRetainsCoverage(t *testing.T) {
	rows := []db.ListEnabledAgentSkillCardMetadataRow{{Name: "fde-growth-log", Description: skillManagedMarker,
		ContentHead: "---\nname: ignored-frontmatter-name\ndescription: " + growthLogDescription + "\n---\nINTERNAL_EXECUTION_BODY"}}
	for i := 0; i < 29; i++ {
		rows = append(rows, db.ListEnabledAgentSkillCardMetadataRow{Name: fmt.Sprintf("other-skill-%02d", i), Description: skillManagedMarker,
			ContentHead: "---\ndescription: " + strings.Repeat("已声明的用途", 20) + "\n---\nSECRET_BODY"})
	}
	query := &coordQueriesStub{skills: rows}
	turn := Turn{AgentID: testAgentID(), Source: SourceWeb, Message: "查询成长日志"}
	(&Coordinator{Queries: query}).FillSkills(context.Background(), &turn)
	if query.skillsCalls != 1 || !query.lastSkillMetadata.IncludeFrontmatter || query.lastSkillMetadata.AgentID != turn.AgentID {
		t.Fatalf("capabilities require exactly one scoped opt-in read: %#v calls=%d", query.lastSkillMetadata, query.skillsCalls)
	}
	if turn.SkillsStatus != "loaded" || len(turn.Skills) != len(rows) || turn.Skills[0].Name != "fde-growth-log" || turn.Skills[0].Description != growthLogDescription {
		t.Fatalf("growth-log capability or installed coverage was lost: %#v", turn.Skills)
	}
	snapshot := formatSkillSnapshots(turn.Skills)
	if utf8.RuneCountInString(snapshot) > skillSnapshotsBudget || !strings.Contains(snapshot, "fde-growth-log: "+growthLogDescription) || strings.Contains(snapshot, "SECRET_BODY") {
		t.Fatal("catalog rendering changed its budget or leaked execution content")
	}
	prompt := buildUserPrompt(turn)
	if !strings.Contains(prompt, "supplied=30") || !strings.Contains(prompt, "catalog_complete=false") || strings.Contains(prompt, "INTERNAL_EXECUTION_BODY") {
		t.Fatal("bounded capability descriptions implied a complete catalog or leaked the body")
	}
}

func TestFillSkillsBadFrontmatterRetainsMetadataAndLoadedState(t *testing.T) {
	query := &coordQueriesStub{skills: []db.ListEnabledAgentSkillCardMetadataRow{
		{Name: "fde-growth-log", Description: skillManagedMarker, ContentHead: "---\ndescription: incomplete"},
		{Name: "empty-description", ContentHead: "# no frontmatter"},
	}}
	turn := Turn{AgentID: testAgentID()}
	(&Coordinator{Queries: query}).FillSkills(context.Background(), &turn)
	if turn.SkillsStatus != "loaded" || len(turn.Skills) != 2 || turn.Skills[0].Description != skillManagedMarker || turn.Skills[1].Description != "" {
		t.Fatalf("malformed metadata changed installed capability state: %#v", turn)
	}
}
