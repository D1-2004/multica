// Package employeedirectory keeps Host directory facts for EmployeeLoop
// wakes: the agent's own address book entry (supervisor, department, title)
// and the member roster of each group scene. Both are read by the Host from
// DingTalk through the agent's DWS identity, never by a model, refreshed at
// most daily under a PostgreSQL lease shared by every replica, and kept when
// a refresh fails. Only directory-public facts are kept: display names,
// titles and departments, never phone numbers, email addresses, personal
// preferences or memory, and title/department only for members the agent's
// own org address book proves.
package employeedirectory

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

// FactKey names one fact about the agent's own account.
type FactKey string

const (
	FactSupervisor FactKey = "supervisor"
	FactDepartment FactKey = "department"
	FactTitle      FactKey = "title"
)

// FactKeys is the stored order of the agent's facts.
var FactKeys = []FactKey{FactSupervisor, FactDepartment, FactTitle}

// FactStatus is what the Host knows about one fact.
type FactStatus string

const (
	// StatusPending: never read successfully.
	StatusPending FactStatus = "pending"
	// StatusKnown: the address book names a value.
	StatusKnown FactStatus = "known"
	// StatusUnregistered: the address book was read and has no value. This
	// is a fact ("no supervisor registered"), not a failure.
	StatusUnregistered FactStatus = "unregistered"
)

// Fact is one stored fact about the agent's own account.
type Fact struct {
	Key    FactKey
	Status FactStatus
	// Value is the display value (a supervisor's name, a department, a title).
	Value string
	// Ref is the org-qualified ref of a supervisor
	// (dingtalk:<org>:staff_id:<id>), "" otherwise.
	Ref         string
	RefreshedAt time.Time
	ErrorCode   string
}

// Profile is the agent's stored directory facts in one tenant org.
type Profile struct {
	WorkspaceID string
	AgentID     string
	TenantOrgID string
	// DWSUID is the identity the facts were read as.
	DWSUID     string
	Supervisor Fact
	Department Fact
	Title      Fact
	// RefreshedAt is the last successful refresh, zero when never.
	RefreshedAt time.Time
	// ErrorCode is the last failed refresh's reason, "" after a success.
	ErrorCode string
}

// Observed is one fact as a directory read returned it.
type Observed struct {
	State ObservedState
	Value string
	Ref   string
}

// ObservedState classifies one read fact.
type ObservedState int

const (
	// ObservedUnavailable: the read could not tell; the stored value stays.
	ObservedUnavailable ObservedState = iota
	// ObservedKnown: the address book names a value.
	ObservedKnown
	// ObservedUnregistered: the address book has no value.
	ObservedUnregistered
)

// SelfEntry is the agent's own address book entry as its identity reads it.
type SelfEntry struct {
	UserID     string
	Supervisor Observed
	Department Observed
	Title      Observed
}

// Member roles in a group.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// MemberOrg classifies a member relative to the scene's tenant org.
type MemberOrg string

const (
	// OrgSame: the agent's own org address book proved this member's staffId.
	OrgSame MemberOrg = "same"
	// OrgOther: the address book does not list this member (another org's
	// member or an unlisted account). Name only.
	OrgOther MemberOrg = "other"
	// OrgUnknown: not checked in this refresh (budget) or the check failed
	// without an older answer. Name only.
	OrgUnknown MemberOrg = "unknown"
)

// Member is one roster entry. Title and Department are set only for OrgSame.
type Member struct {
	// Ref is the member's org-qualified ref as the agent's identity sees
	// them: dingtalk:<tenant org>:open_id:<openDingTalkId>.
	Ref string `json:"ref"`
	// StaffRef is dingtalk:<tenant org>:staff_id:<staffId>, proved by the
	// address book; OrgSame members only.
	StaffRef   string    `json:"staff_ref,omitempty"`
	Name       string    `json:"name"`
	GroupNick  string    `json:"group_nick,omitempty"`
	Role       string    `json:"role"`
	Org        MemberOrg `json:"org"`
	Title      string    `json:"title,omitempty"`
	Department string    `json:"department,omitempty"`
	// Self marks the agent's own account.
	Self bool `json:"self,omitempty"`
}

// Roster is a group scene's stored member roster.
type Roster struct {
	WorkspaceID string
	AgentID     string
	SceneID     string
	TenantOrgID string
	DWSUID      string
	// Members is bounded (StoreMax); Total counts the whole group.
	Members     []Member
	Total       int
	RefreshedAt time.Time
	ErrorCode   string
}

// GroupMember is one member as the group's member list returned it.
type GroupMember struct {
	OpenDingTalkID string
	Name           string
	GroupNick      string
	Role           string
}

// Person is one address book entry read by staffId.
type Person struct {
	StaffID    string
	Name       string
	Title      string
	Department string
}

// Directory reads DingTalk as one execution identity. Implementations never
// return phone numbers or email addresses.
type Directory interface {
	// Self reads the identity's own address book entry.
	Self(ctx context.Context, id dwsclient.Identity) (SelfEntry, error)
	// GroupMembers lists a group conversation's members.
	GroupMembers(ctx context.Context, id dwsclient.Identity, conversationID string) ([]GroupMember, error)
	// ProveStaff returns the staffId the identity's own address book proves
	// for exactly openDingTalkID ("" when it lists no such colleague). Names
	// only narrow the search; they never decide who someone is.
	ProveStaff(ctx context.Context, id dwsclient.Identity, openDingTalkID string, names []string) (string, error)
	// Users reads address book entries by staffId; entries the identity
	// cannot see are left out.
	Users(ctx context.Context, id dwsclient.Identity, staffIDs []string) ([]Person, error)
}

// Ref forms.
func openRef(org, openDingTalkID string) string {
	return "dingtalk:" + org + ":open_id:" + openDingTalkID
}

func staffRef(org, staffID string) string {
	return "dingtalk:" + org + ":staff_id:" + staffID
}

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// Mainland mobile numbers and long digit runs that look like phone numbers.
	phonePattern = regexp.MustCompile(`(?:\+?86[\s\-]?)?1[3-9]\d[\s\-]?\d{4}[\s\-]?\d{4}|\d{3,4}-\d{7,8}`)
	idLike       = regexp.MustCompile(`^[A-Za-z0-9_\-+=/.:]{1,128}$`)
)

// publicText is a directory display value fit to store and render:
// single-line, clipped, with anything that looks like a phone number or an
// email address removed.
func publicText(s string, maxRunes int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" || strings.EqualFold(s, "null") || strings.EqualFold(s, "none") {
		return ""
	}
	// Contact details are cut out, never kept or shown.
	s = emailPattern.ReplaceAllString(s, "")
	s = phonePattern.ReplaceAllString(s, "")
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	s = strings.NewReplacer("()", "", "（）", "", "[]", "", "【】", "").Replace(s)
	s = strings.Trim(strings.TrimSpace(s), ",;:，；：、")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	if utf8.RuneCountInString(s) > maxRunes {
		r := []rune(s)
		s = string(r[:maxRunes-1]) + "…"
	}
	return s
}

// cleanID is an external id fit to keep in a ref, "" otherwise.
func cleanID(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "null") || !idLike.MatchString(s) {
		return ""
	}
	return s
}

// Display bounds.
const (
	maxNameRunes  = 32
	maxTitleRunes = 40
	maxDeptRunes  = 48
)
