package dws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// GroupService reads conversations and their members.
type GroupService struct{ c *Client }

// Group is a conversation as seen by the identity.
type Group struct {
	ConversationID string `json:"conversationId"`
	Title          string `json:"title,omitempty"`
	MemberCount    int    `json:"memberCount,omitempty"`
	SingleChat     bool   `json:"singleChat,omitempty"`
	CreateTime     string `json:"createTime,omitempty"`
	GroupType      string `json:"groupType,omitempty"`
	OwnerID        string `json:"ownerOpenDingTalkId,omitempty"`
}

// Member is one member of a group. OpenDingTalkID is relative to the caller.
type Member struct {
	OpenDingTalkID string `json:"openDingTalkId"`
	Name           string `json:"name,omitempty"`
	GroupNick      string `json:"groupNick,omitempty"`
	Role           string `json:"role,omitempty"`
}

// Info returns the conversation's title and size. get_conversation_info
// refuses some internal groups (FORBIDDEN, measured on the staging gateway)
// even to their owner; Info then looks the group up in the identity's group
// list and, failing that, counts its members, leaving Title empty.
func (s *GroupService) Info(ctx context.Context, conversationID string) (Group, error) {
	if conversationID == "" {
		return Group{}, invalid("group info needs conversationId")
	}
	raw, err := s.c.Call(ctx, ServerChat, "get_conversation_info", map[string]any{"openConversationId": conversationID})
	var denied *Error
	if errors.As(err, &denied) && denied.Code == "FORBIDDEN" {
		return s.infoFallback(ctx, conversationID)
	}
	if err != nil {
		return Group{}, err
	}
	var out struct {
		Info struct {
			OpenConversationID string `json:"openConversationId"`
			Title              string `json:"title"`
			MemberCount        int    `json:"memberCount"`
			SingleChat         bool   `json:"singleChat"`
			CreateAt           string `json:"createAt"`
		} `json:"conversationInfo"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Group{}, fmt.Errorf("dws: decode conversation info: %w", err)
	}
	i := out.Info
	if i.OpenConversationID == "" {
		i.OpenConversationID = conversationID
	}
	return Group{ConversationID: i.OpenConversationID, Title: i.Title, MemberCount: i.MemberCount, SingleChat: i.SingleChat, CreateTime: i.CreateAt}, nil
}

const maxGroupListPages = 5

func (s *GroupService) infoFallback(ctx context.Context, conversationID string) (Group, error) {
	cursor := any(nil)
	for page := 0; page < maxGroupListPages; page++ {
		args := map[string]any{"limit": 100}
		if cursor != nil {
			args["cursor"] = cursor
		}
		raw, err := s.c.Call(ctx, ServerIM, "list_my_groups_pagination", args)
		if err != nil {
			break
		}
		var pg struct {
			Groups     []wireGroup `json:"groups"`
			HasMore    bool        `json:"hasMore"`
			NextCursor any         `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &pg) != nil {
			break
		}
		for _, g := range pg.Groups {
			if g.OpenConversationID == conversationID {
				return g.group(), nil
			}
		}
		if !pg.HasMore || pg.NextCursor == nil {
			break
		}
		cursor = pg.NextCursor
	}
	members, err := s.Members(ctx, conversationID)
	if err != nil {
		return Group{}, err
	}
	return Group{ConversationID: conversationID, MemberCount: len(members)}, nil
}

type wireGroup struct {
	OpenConversationID  string `json:"openConversationId"`
	Title               string `json:"title"`
	MemberCount         int    `json:"memberCount"`
	CreateAt            string `json:"createAt"`
	GroupType           string `json:"groupType"`
	OwnerOpenDingtalkID string `json:"ownerOpenDingtalkId"`
}

func (g wireGroup) group() Group {
	return Group{ConversationID: g.OpenConversationID, Title: g.Title, MemberCount: g.MemberCount,
		CreateTime: g.CreateAt, GroupType: g.GroupType, OwnerID: g.OwnerOpenDingtalkID}
}

const maxMemberPages = 50

// Members lists every member of a group, following the cursor.
func (s *GroupService) Members(ctx context.Context, conversationID string) ([]Member, error) {
	if conversationID == "" {
		return nil, invalid("members needs conversationId")
	}
	members := []Member{}
	seen := map[string]bool{}
	cursor := "0"
	for page := 0; page < maxMemberPages; page++ {
		raw, err := s.c.Call(ctx, ServerChat, "get_group_members", map[string]any{"openconversation_id": conversationID, "cursor": cursor})
		if err != nil {
			return nil, err
		}
		var pg struct {
			List []struct {
				OpenDingtalkID  string `json:"openDingtalkId"`
				MemberNick      string `json:"memberNick"`
				MemberEmpName   string `json:"memberEmpName"`
				MemberGroupNick string `json:"memberGroupNick"`
				MemberRoleDesc  string `json:"memberRoleDesc"`
			} `json:"list"`
			HasMore    bool            `json:"hasMore"`
			NextCursor json.RawMessage `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &pg); err != nil {
			return nil, fmt.Errorf("dws: decode members: %w", err)
		}
		for _, m := range pg.List {
			if m.OpenDingtalkID == "" || seen[m.OpenDingtalkID] {
				continue
			}
			seen[m.OpenDingtalkID] = true
			name := m.MemberEmpName
			if name == "" {
				name = m.MemberNick
			}
			members = append(members, Member{OpenDingTalkID: m.OpenDingtalkID, Name: name, GroupNick: m.MemberGroupNick, Role: m.MemberRoleDesc})
		}
		next := strings.Trim(string(pg.NextCursor), `"`)
		if !pg.HasMore || next == "" || next == "null" || next == cursor {
			break
		}
		cursor = next
	}
	return members, nil
}

// Find searches the identity's groups by name.
func (s *GroupService) Find(ctx context.Context, keyword string) ([]Group, error) {
	if strings.TrimSpace(keyword) == "" {
		return nil, invalid("find group needs a keyword")
	}
	raw, err := s.c.Call(ctx, ServerIM, "search_groups", map[string]any{"keyword": keyword, "limit": 20})
	if err != nil {
		return nil, err
	}
	var out struct {
		Groups []wireGroup `json:"groups"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("dws: decode groups: %w", err)
	}
	groups := make([]Group, 0, len(out.Groups))
	for _, g := range out.Groups {
		groups = append(groups, g.group())
	}
	return groups, nil
}
