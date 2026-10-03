package employeedirectory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/dws"
)

// DWSDirectory reads the address book and group members through an
// identity's shared DWS SDK client (the same session its native event
// stream and replies use). Only the fields this package keeps are decoded:
// phone numbers, email addresses, job numbers and avatars never are.
type DWSDirectory struct {
	Client func(ctx context.Context, id dwsclient.Identity) (*dws.Client, error)
}

// usersBatch bounds one get_user_info_by_user_ids call.
const usersBatch = 20

type wireDept struct {
	DeptID   json.RawMessage `json:"deptId"`
	DeptName string          `json:"deptName"`
}

type wirePosition struct {
	DeptID json.RawMessage `json:"deptId"`
	IsMain bool            `json:"isMain"`
	Title  *string         `json:"title"`
}

// wireEmployee is the orgEmployeeModel of get_user_info_by_user_ids and
// get_current_user_profile, restricted to directory-public fields.
type wireEmployee struct {
	UserID               string         `json:"userId"`
	OrgUserID            string         `json:"orgUserId"`
	OrgUserName          string         `json:"orgUserName"`
	OrgTitle             *string        `json:"orgTitle"`
	OrgMasterUserID      *string        `json:"orgMasterUserId"`
	OrgMasterDisplayName *string        `json:"orgMasterDisplayName"`
	Depts                []wireDept     `json:"depts"`
	Positions            []wirePosition `json:"positions"`
}

type wireUserRow struct {
	Employee wireEmployee `json:"orgEmployeeModel"`
}

func (e wireEmployee) id() string {
	if id := cleanID(e.OrgUserID); id != "" {
		return id
	}
	return cleanID(e.UserID)
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func rawID(raw json.RawMessage) string {
	return strings.Trim(strings.TrimSpace(string(raw)), `"`)
}

// title is the org title, else the main position's title.
func (e wireEmployee) title() string {
	if t := publicText(str(e.OrgTitle), maxTitleRunes); t != "" {
		return t
	}
	for _, p := range e.Positions {
		if p.IsMain {
			return publicText(str(p.Title), maxTitleRunes)
		}
	}
	return ""
}

// department is the main position's department, else the first listed.
func (e wireEmployee) department() string {
	main := ""
	for _, p := range e.Positions {
		if p.IsMain {
			main = rawID(p.DeptID)
		}
	}
	for _, d := range e.Depts {
		if main != "" && rawID(d.DeptID) == main {
			return publicText(d.DeptName, maxDeptRunes)
		}
	}
	for _, d := range e.Depts {
		if name := publicText(d.DeptName, maxDeptRunes); name != "" {
			return name
		}
	}
	return ""
}

func (d DWSDirectory) client(ctx context.Context, id dwsclient.Identity) (*dws.Client, error) {
	if d.Client == nil {
		return nil, errors.New("DWS directory is not configured")
	}
	return d.Client(ctx, id)
}

func (d DWSDirectory) users(ctx context.Context, client *dws.Client, staffIDs []string) ([]wireEmployee, error) {
	var out []wireEmployee
	for start := 0; start < len(staffIDs); start += usersBatch {
		end := min(start+usersBatch, len(staffIDs))
		raw, err := client.Call(ctx, dws.ServerContact, "get_user_info_by_user_ids", map[string]any{"user_id_list": staffIDs[start:end]})
		if err != nil {
			return nil, err
		}
		var rows []wireUserRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("decode user info: %w", err)
		}
		for _, r := range rows {
			out = append(out, r.Employee)
		}
	}
	return out, nil
}

// Self reads the identity's own entry by its staffId (the DWS uid of an
// execution identity is its org userId) and falls back to
// get_current_user_profile when the entry is not visible that way. Either
// answer must be the identity's own account.
func (d DWSDirectory) Self(ctx context.Context, id dwsclient.Identity) (SelfEntry, error) {
	uid := cleanID(id.UID)
	if uid == "" {
		return SelfEntry{}, errors.New("DWS identity has no uid")
	}
	client, err := d.client(ctx, id)
	if err != nil {
		return SelfEntry{}, err
	}
	rows, err := d.users(ctx, client, []string{uid})
	if err != nil {
		return SelfEntry{}, err
	}
	var self *wireEmployee
	for i := range rows {
		if rows[i].id() == uid {
			self = &rows[i]
		}
	}
	titleKnown := self != nil
	if self == nil {
		raw, err := client.Call(ctx, dws.ServerContact, "get_current_user_profile", nil)
		if err != nil {
			return SelfEntry{}, err
		}
		var profile []wireUserRow
		if err := json.Unmarshal(raw, &profile); err != nil || len(profile) == 0 {
			return SelfEntry{}, errors.New("unexpected get_current_user_profile result")
		}
		self = &profile[0].Employee
	}
	if self.id() != uid {
		return SelfEntry{}, ErrIdentityMismatch
	}
	entry := SelfEntry{UserID: uid}
	supervisorID := cleanID(str(self.OrgMasterUserID))
	supervisorName := publicText(str(self.OrgMasterDisplayName), maxNameRunes)
	switch {
	case supervisorID == "" && supervisorName == "":
		entry.Supervisor = Observed{State: ObservedUnregistered}
	default:
		if supervisorName == "" && supervisorID != "" {
			// The entry names the supervisor only by id: read their name.
			if people, err := d.users(ctx, client, []string{supervisorID}); err == nil {
				for _, p := range people {
					if p.id() == supervisorID {
						supervisorName = publicText(p.OrgUserName, maxNameRunes)
					}
				}
			}
		}
		ref := ""
		if supervisorID != "" {
			ref = staffRef(id.OrgID, supervisorID)
		}
		entry.Supervisor = Observed{State: ObservedKnown, Value: supervisorName, Ref: ref}
	}
	if dept := self.department(); dept != "" {
		entry.Department = Observed{State: ObservedKnown, Value: dept}
	} else {
		entry.Department = Observed{State: ObservedUnregistered}
	}
	switch title := self.title(); {
	case title != "":
		entry.Title = Observed{State: ObservedKnown, Value: title}
	case titleKnown:
		entry.Title = Observed{State: ObservedUnregistered}
	default:
		// get_current_user_profile carries no title: this read cannot tell.
		entry.Title = Observed{State: ObservedUnavailable}
	}
	return entry, nil
}

// ErrIdentityMismatch: the directory answered for another account.
var ErrIdentityMismatch = errors.New("directory entry is not the identity's own account")

// GroupMembers lists every member of a group conversation.
func (d DWSDirectory) GroupMembers(ctx context.Context, id dwsclient.Identity, conversationID string) ([]GroupMember, error) {
	client, err := d.client(ctx, id)
	if err != nil {
		return nil, err
	}
	members, err := client.Groups.Members(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	out := make([]GroupMember, 0, len(members))
	for _, m := range members {
		out = append(out, GroupMember{OpenDingTalkID: m.OpenDingTalkID, Name: m.Name, GroupNick: m.GroupNick, Role: roleOf(m.Role)})
	}
	return out, nil
}

// roleOf maps DingTalk's member role description (群主, 管理员, 普通成员).
func roleOf(desc string) string {
	desc = strings.TrimSpace(desc)
	switch {
	case strings.Contains(desc, "群主") || strings.EqualFold(desc, "owner"):
		return RoleOwner
	case strings.Contains(desc, "管理员") || strings.EqualFold(desc, "admin"):
		return RoleAdmin
	default:
		return RoleMember
	}
}

// ProveStaff is dws.ContactService.StaffIDOf without a group lookup (the
// caller passes the member's names).
func (d DWSDirectory) ProveStaff(ctx context.Context, id dwsclient.Identity, openDingTalkID string, names []string) (string, error) {
	client, err := d.client(ctx, id)
	if err != nil {
		return "", err
	}
	return client.Contacts.StaffIDOf(ctx, openDingTalkID, names, "")
}

// Users reads directory-public fields of staffIds.
func (d DWSDirectory) Users(ctx context.Context, id dwsclient.Identity, staffIDs []string) ([]Person, error) {
	client, err := d.client(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := d.users(ctx, client, staffIDs)
	if err != nil {
		return nil, err
	}
	out := make([]Person, 0, len(rows))
	for _, r := range rows {
		if r.id() == "" {
			continue
		}
		out = append(out, Person{StaffID: r.id(), Name: publicText(r.OrgUserName, maxNameRunes), Title: r.title(), Department: r.department()})
	}
	return out, nil
}

// classify maps a directory failure onto a stored error code.
func classify(err error) string {
	var dwsErr *dws.Error
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrIdentityMismatch):
		return "identity_mismatch"
	case errors.As(err, &dwsErr) && dwsErr.Code == "FORBIDDEN":
		return "forbidden"
	case dws.IsAuth(err) || errors.Is(err, dws.ErrSessionExpired):
		return "auth"
	default:
		return "provider_error"
	}
}
