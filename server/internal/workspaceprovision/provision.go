package workspaceprovision

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agenttemplate"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var nonAlpha = regexp.MustCompile(`[^a-zA-Z]`)

type Step string

const (
	StepWorkspace Step = "workspace"
	StepOwner     Step = "owner"
	StepTemplate  Step = "template"
)

type Error struct {
	Step Step
	Err  error
}

func (e *Error) Error() string { return "provision workspace " + string(e.Step) + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

type Queries interface {
	CreateWorkspace(context.Context, db.CreateWorkspaceParams) (db.Workspace, error)
	CreateMember(context.Context, db.CreateMemberParams) (db.Member, error)
	CreateWorkspaceTemplateFromSeed(context.Context, db.CreateWorkspaceTemplateFromSeedParams) (db.AgentTemplate, error)
}

type Params struct {
	Name        string
	Slug        string
	Description pgtype.Text
	Context     pgtype.Text
	IssuePrefix string
	OwnerID     pgtype.UUID
}

func Create(ctx context.Context, q Queries, params Params) (db.Workspace, error) {
	ws, err := q.CreateWorkspace(ctx, db.CreateWorkspaceParams{
		Name: params.Name, Slug: params.Slug, Description: params.Description,
		Context: params.Context, IssuePrefix: params.IssuePrefix,
	})
	if err != nil {
		return db.Workspace{}, &Error{Step: StepWorkspace, Err: err}
	}
	if _, err := q.CreateMember(ctx, db.CreateMemberParams{
		WorkspaceID: ws.ID, UserID: params.OwnerID, Role: "owner",
	}); err != nil {
		return db.Workspace{}, &Error{Step: StepOwner, Err: err}
	}
	if _, err := q.CreateWorkspaceTemplateFromSeed(ctx, db.CreateWorkspaceTemplateFromSeedParams{
		WorkspaceID: ws.ID, Slug: agenttemplate.DefaultSlug,
		CreatedBy: params.OwnerID, SystemKey: agenttemplate.DefaultSystemKey,
	}); err != nil {
		return db.Workspace{}, &Error{Step: StepTemplate, Err: err}
	}
	return ws, nil
}

// GenerateIssuePrefix produces the stable 2-3 letter default used by normal
// and product-provisioned workspaces.
func GenerateIssuePrefix(name string) string {
	letters := nonAlpha.ReplaceAllString(name, "")
	if len(letters) == 0 {
		return "WS"
	}
	letters = strings.ToUpper(letters)
	if len(letters) > 3 {
		letters = letters[:3]
	}
	return letters
}
