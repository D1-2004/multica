package dws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func standardGateway(tool string, args map[string]any) (int, string) {
	switch tool {
	case "send_personal_message":
		return ok(`{"openMessageId":"m-new","openTaskId":"task-1"}`)
	case "list_messages_by_ids":
		return ok(`{"messages":[{"openMessageId":"m-origin","senderOpenDingTalkId":"sender-1","content":"hi",
			"quotedMessage":{"openMessageId":"m-q","content":"earlier"}}]}`)
	case "create_and_send_card":
		return ok(`{"bizId":"card-1","openTaskId":"task-card"}`)
	case "get_current_user_profile":
		return ok(meResult)
	default:
		return ok(`{}`)
	}
}

func TestSendRendersMentionsTitleAndIdempotencyKey(t *testing.T) {
	g, c := newTestClient(t, standardGateway)
	sent, err := c.Messages.Send(context.Background(), SendRequest{Target: Target{ConversationID: "cid-1"}, Text: "**进展** 好了", AtOpenDingTalkIDs: []string{"u1"}})
	if err != nil || sent.MessageID != "m-new" || sent.UUID == "" {
		t.Fatalf("sent=%+v err=%v", sent, err)
	}
	args := g.call("send_personal_message").Args
	content := mustJSON(t, args["content"].(string))
	if content["text"] != "<@u1> **进展** 好了" || content["title"] != "进展 好了" || args["uuid"] != sent.UUID {
		t.Fatalf("args=%v content=%v", args, content)
	}
}

func TestReplyResolvesTheQuotedSender(t *testing.T) {
	g, c := newTestClient(t, standardGateway)
	if _, err := c.Messages.Reply(context.Background(), ReplyRequest{ConversationID: "cid-1", MessageID: "m-origin", Text: "收到"}); err != nil {
		t.Fatal(err)
	}
	if got := g.tools(); !reflect.DeepEqual(got, []string{"list_messages_by_ids", "send_personal_message"}) {
		t.Fatalf("tools = %v", got)
	}
	content := mustJSON(t, g.call("send_personal_message").Args["content"].(string))
	if content["srcMsgSendOpenDingTalkId"] != "sender-1" || content["replyMsgType"] != "markdown" {
		t.Fatalf("content = %v", content)
	}
}

func TestGetKeepsTheQuotedMessage(t *testing.T) {
	_, c := newTestClient(t, standardGateway)
	msgs, err := c.Messages.Get(context.Background(), "m-origin")
	if err != nil || len(msgs) != 1 || msgs[0].Quoted == nil || msgs[0].Quoted.Content != "earlier" {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
}

// historyServer serves list_conversation_message_v2 like the gateway: pages
// of messages strictly older than "time" (second precision), newest first,
// with nextCursor set to the oldest returned message.
func historyServer(msgs []wireMessage) func(tool string, args map[string]any) (int, string) {
	return func(tool string, args map[string]any) (int, string) {
		before, _ := time.ParseInLocation("2006-01-02 15:04:05", args["time"].(string), shanghai)
		limit := int(args["limit"].(float64))
		var page []wireMessage
		for _, m := range msgs { // msgs is newest first
			at, _ := parseCreateTime(m.CreateTime)
			if at.Before(before) && len(page) < limit {
				page = append(page, m)
			}
		}
		more := len(page) == limit
		next := int64(0)
		if len(page) > 0 {
			at, _ := parseCreateTime(page[len(page)-1].CreateTime)
			next = at.UnixMilli()
		}
		raw, _ := json.Marshal(map[string]any{"messages": page, "nextCursor": next, "hasMore": more})
		return ok(string(raw))
	}
}

func makeHistory(n int, base time.Time) []wireMessage {
	msgs := make([]wireMessage, 0, n)
	for i := n - 1; i >= 0; i-- {
		at := base.Add(time.Duration(i) * time.Second)
		msgs = append(msgs, wireMessage{OpenMessageID: fmt.Sprintf("m%03d", i), Content: fmt.Sprintf("msg %d", i),
			CreateTime: at.In(shanghai).Format("2006-01-02 15:04:05")})
	}
	return msgs
}

func TestHistoryReadsEveryMessageAcrossPagesOldestFirst(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	msgs := makeHistory(250, base)
	// Two messages in the same second straddle the first page boundary; an
	// exact cursor with a strict "older than" loses the second one.
	msgs[100].CreateTime = msgs[99].CreateTime
	g, c := newTestClient(t, historyServer(msgs))
	got, err := c.Messages.History(context.Background(), HistoryQuery{ConversationID: "cid-1", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 250 {
		t.Fatalf("got %d messages, want 250", len(got))
	}
	seen := map[string]bool{}
	for i, m := range got {
		if seen[m.MessageID] {
			t.Fatalf("duplicate %s", m.MessageID)
		}
		seen[m.MessageID] = true
		if i > 0 && m.CreateTime < got[i-1].CreateTime {
			t.Fatal("history must be oldest first")
		}
	}
	if len(g.calls) < 3 {
		t.Fatalf("expected paging, got %d calls", len(g.calls))
	}
}

func TestHistoryHonorsSinceAndLimit(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	_, c := newTestClient(t, historyServer(makeHistory(250, base)))
	recent, err := c.Messages.History(context.Background(), HistoryQuery{ConversationID: "cid-1", Since: base.Add(240 * time.Second), Limit: 1000})
	if err != nil || len(recent) != 10 || recent[0].MessageID != "m240" {
		t.Fatalf("since: n=%d err=%v", len(recent), err)
	}
	last5, _ := c.Messages.History(context.Background(), HistoryQuery{ConversationID: "cid-1", Limit: 5})
	if len(last5) != 5 || last5[4].MessageID != "m249" || last5[0].MessageID != "m245" {
		t.Fatalf("limit keeps the newest: %+v", last5)
	}
}

// msHistoryServer is historyServer with millisecond timestamps: "time" may
// carry milliseconds, and nextCursor is the oldest message's exact time.
func msHistoryServer(times []time.Time) func(tool string, args map[string]any) (int, string) {
	return func(tool string, args map[string]any) (int, string) {
		raw := args["time"].(string)
		before, err := time.ParseInLocation("2006-01-02 15:04:05.000", raw, shanghai)
		if err != nil {
			before, _ = time.ParseInLocation("2006-01-02 15:04:05", raw, shanghai)
		}
		limit := int(args["limit"].(float64))
		var page []wireMessage
		var last time.Time
		for i := len(times) - 1; i >= 0 && len(page) < limit; i-- { // newest first
			if times[i].Before(before) {
				page = append(page, wireMessage{OpenMessageID: fmt.Sprintf("m%03d", i), CreateTime: times[i].In(shanghai).Format("2006-01-02 15:04:05")})
				last = times[i]
			}
		}
		next := int64(0)
		if len(page) > 0 {
			next = last.UnixMilli()
		}
		body, _ := json.Marshal(map[string]any{"messages": page, "nextCursor": next, "hasMore": len(page) == limit})
		return ok(string(body))
	}
}

func TestHistoryPagesThroughAWholeSecondOfMessages(t *testing.T) {
	// 150 messages in one second, then 50 older ones: the one-second overlap
	// alone would re-read the same page forever and stop at 100.
	sec := time.Now().Add(-time.Hour).Truncate(time.Second)
	var times []time.Time
	for i := 0; i < 50; i++ {
		times = append(times, sec.Add(-time.Duration(50-i)*time.Second))
	}
	for i := 0; i < 150; i++ {
		times = append(times, sec.Add(time.Duration(i*5)*time.Millisecond))
	}
	_, c := newTestClient(t, msHistoryServer(times))
	got, err := c.Messages.History(context.Background(), HistoryQuery{ConversationID: "cid-1", Limit: 1000})
	if err != nil || len(got) != 200 {
		t.Fatalf("got %d messages, want 200 (err %v)", len(got), err)
	}
}

func TestSearchReportsTruncation(t *testing.T) {
	// More history than the scan budget, all inside the default 7 days.
	base := time.Now().Add(-6 * 24 * time.Hour)
	var times []time.Time
	for i := 0; i < searchMaxPages*historyPageSize+10; i++ {
		times = append(times, base.Add(time.Duration(i)*time.Minute))
	}
	_, c := newTestClient(t, msHistoryServer(times))
	res, err := c.Messages.Search(context.Background(), SearchQuery{ConversationID: "cid-1", Keyword: "nothing matches"})
	if err != nil || !res.Truncated || res.Messages == nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestSearchScansHistory(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	msgs := makeHistory(30, base)
	msgs[3].Content, msgs[20].Content = "WS-214 进行中", "ws-214 已完成"
	_, c := newTestClient(t, historyServer(msgs))
	res, err := c.Messages.Search(context.Background(), SearchQuery{ConversationID: "cid-1", Keyword: "ws-214"})
	if err != nil || len(res.Messages) != 2 || res.Messages[0].Content != "ws-214 已完成" || res.Truncated {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestValidationErrorsSendNothing(t *testing.T) {
	g, c := newTestClient(t, standardGateway)
	ctx := context.Background()
	errs := []error{
		func() error { _, err := c.Messages.Send(ctx, SendRequest{Text: "x"}); return err }(),
		func() error {
			_, err := c.Messages.Reply(ctx, ReplyRequest{ConversationID: "c", Text: "x"})
			return err
		}(),
		func() error { _, err := c.Messages.History(ctx, HistoryQuery{}); return err }(),
		func() error { _, err := c.Messages.Search(ctx, SearchQuery{ConversationID: "c"}); return err }(),
		func() error { _, err := c.Groups.Info(ctx, ""); return err }(),
		func() error { _, err := c.Groups.Find(ctx, " "); return err }(),
		c.Messages.Recall(ctx, "c", ""),
	}
	for i, err := range errs {
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("case %d: err = %v", i, err)
		}
	}
	if len(g.calls) != 0 {
		t.Fatalf("calls = %v", g.tools())
	}
}

func TestGroups(t *testing.T) {
	page := 0
	g, c := newTestClient(t, func(tool string, args map[string]any) (int, string) {
		switch tool {
		case "get_conversation_info":
			return ok(`{"conversationInfo":{"openConversationId":"cid-1","title":"项目群","memberCount":3,"singleChat":false,"createAt":"2026-09-29 20:51:02"}}`)
		case "get_group_members":
			page++
			if args["cursor"] == "0" {
				return ok(`{"list":[{"openDingtalkId":"a","memberEmpName":"甲","memberRoleDesc":"群主"},{"openDingtalkId":"b","memberNick":"乙"}],"hasMore":true,"nextCursor":"p2"}`)
			}
			return ok(`{"list":[{"openDingtalkId":"c","memberEmpName":"丙"},{"openDingtalkId":"a","memberEmpName":"甲"}],"hasMore":false}`)
		case "search_groups":
			return ok(`{"groups":[{"openConversationId":"cid-1","title":"项目群","memberCount":3,"ownerOpenDingtalkId":"a"}]}`)
		}
		return ok(`{}`)
	})
	info, err := c.Groups.Info(context.Background(), "cid-1")
	if err != nil || info.Title != "项目群" || info.MemberCount != 3 {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	members, err := c.Groups.Members(context.Background(), "cid-1")
	if err != nil || len(members) != 3 || members[0].Role != "群主" || members[1].Name != "乙" {
		t.Fatalf("members=%+v err=%v", members, err)
	}
	if g.calls[2].Args["cursor"] != "p2" {
		t.Fatalf("second page cursor = %v", g.calls[2].Args["cursor"])
	}
	groups, err := c.Groups.Find(context.Background(), "项目")
	if err != nil || len(groups) != 1 || groups[0].OwnerID != "a" {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
}

func TestGroupInfoFallsBackWhenTheGroupIsForbidden(t *testing.T) {
	listed := false
	_, c := newTestClient(t, func(tool string, args map[string]any) (int, string) {
		switch tool {
		case "get_conversation_info":
			// Measured on the staging gateway for an internal group.
			return 200, toolText(`{"success":false,"errorCode":"FORBIDDEN","errorMsg":"无权访问该内部群","result":{}}`)
		case "list_my_groups_pagination":
			if listed {
				return ok(`{"groups":[{"openConversationId":"cid-1","title":"项目群","memberCount":3}],"hasMore":false}`)
			}
			return ok(`{"groups":[{"openConversationId":"cid-other","title":"别的群"}],"hasMore":false}`)
		case "get_group_members":
			return ok(`{"list":[{"openDingtalkId":"a"},{"openDingtalkId":"b"}],"hasMore":false}`)
		}
		return ok(`{}`)
	})
	info, err := c.Groups.Info(context.Background(), "cid-1")
	if err != nil || info.MemberCount != 2 || info.Title != "" {
		t.Fatalf("members fallback: %+v %v", info, err)
	}
	listed = true
	info, err = c.Groups.Info(context.Background(), "cid-1")
	if err != nil || info.Title != "项目群" {
		t.Fatalf("group list fallback: %+v %v", info, err)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, shanghai)
	for in, want := range map[string]time.Time{
		"30m":                  now.Add(-30 * time.Minute),
		"2h":                   now.Add(-2 * time.Hour),
		"7d":                   now.AddDate(0, 0, -7),
		"2026-09-29":           time.Date(2026, 9, 29, 0, 0, 0, 0, shanghai),
		"2026-09-29 10:00":     time.Date(2026, 9, 29, 10, 0, 0, 0, shanghai),
		"2026-09-29T10:00:00Z": time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC),
	} {
		if got, err := ParseSince(in, now); err != nil || !got.Equal(want) {
			t.Errorf("%s: got %v err %v", in, got, err)
		}
	}
	if _, err := ParseSince("yesterday", now); !errors.Is(err, ErrInvalidRequest) {
		t.Error("bad since must be an invalid request")
	}
}

// A staffId comes only from an address book entry with exactly the sender's
// openDingTalkId; a namesake never decides, and a group member's nick is
// tried after the sender name.
func TestStaffIDOf(t *testing.T) {
	g, c := newTestClient(t, func(tool string, args map[string]any) (int, string) {
		switch tool {
		case "search_contact_by_key_word":
			switch args["keyword"] {
			case "夏东翔":
				// A namesake in the org and the person as an external friend.
				return ok(`[{"userId":"0138","openDingTalkId":"open-namesake","name":"xdx"},{"userId":null,"openDingTalkId":"open-me","name":"夏东翔"}]`)
			case "冬翔":
				return ok(`[{"userId":"103262","openDingTalkId":"open-me","name":"夏东翔","nick":"冬翔"}]`)
			}
			return ok(`[]`)
		case "list_group_member_by_ids":
			if args["openConversationId"] != "cid-1" {
				t.Fatalf("members of %v", args["openConversationId"])
			}
			return ok(`{"members":[{"openDingtalkId":"open-me","nick":"冬翔","groupNick":"群里的冬翔"}]}`)
		}
		return ok(`{}`)
	})
	id, err := c.Contacts.StaffIDOf(context.Background(), "open-me", []string{"冬翔"}, "")
	if err != nil || id != "103262" {
		t.Fatalf("by sender name: %q %v", id, err)
	}
	// The sender name finds only a namesake and the friend entry: the nick
	// from the group settles it.
	id, err = c.Contacts.StaffIDOf(context.Background(), "open-me", []string{"夏东翔", "null"}, "cid-1")
	if err != nil || id != "103262" {
		t.Fatalf("by group nick: %q %v", id, err)
	}
	// Outside a group, no entry with the id: nobody.
	id, err = c.Contacts.StaffIDOf(context.Background(), "open-me", []string{"夏东翔"}, "")
	if err != nil || id != "" {
		t.Fatalf("namesake only: %q %v", id, err)
	}
	if _, err := c.Contacts.StaffIDOf(context.Background(), " ", nil, ""); err == nil {
		t.Fatal("empty openDingTalkId accepted")
	}
	searches := 0
	for _, call := range g.calls {
		if call.Tool == "search_contact_by_key_word" {
			searches++
		}
	}
	if searches != 4 {
		t.Fatalf("searches = %d, want 4 (the literal null and repeated names are not searched)", searches)
	}
}

// The literal "null" is no userId, and a group that refuses its member list
// is a miss, not a failure.
func TestStaffIDOfRefusals(t *testing.T) {
	_, c := newTestClient(t, func(tool string, args map[string]any) (int, string) {
		switch tool {
		case "search_contact_by_key_word":
			return ok(`[{"userId":"null","openDingTalkId":"open-me","name":"x"}]`)
		case "list_group_member_by_ids":
			return 200, toolText(`{"success":false,"errorCode":"FORBIDDEN","errorMsg":"无权访问该内部群","result":{}}`)
		}
		return ok(`{}`)
	})
	id, err := c.Contacts.StaffIDOf(context.Background(), "open-me", []string{"x"}, "cid-internal")
	if err != nil || id != "" {
		t.Fatalf("staff id = %q err = %v, want a miss", id, err)
	}
}
