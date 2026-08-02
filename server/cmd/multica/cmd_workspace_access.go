package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
)

const workspaceAccessTokenPrefix = "dta_"

type workspaceAccessSelf struct {
	PrincipalType string `json:"principal_type"`
	TokenID       string `json:"token_id"`
	Name          string `json:"name"`
	WorkspaceID   string `json:"workspace_id"`
	Workspace     struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		Slug        string  `json:"slug"`
		Description *string `json:"description"`
		Context     *string `json:"context"`
		IssuePrefix string  `json:"issue_prefix"`
	} `json:"workspace"`
	Capabilities  []string `json:"capabilities"`
	ResourceScope string   `json:"resource_scope"`
	Version       int32    `json:"version"`
}

func isWorkspaceAccessToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), workspaceAccessTokenPrefix)
}

func fetchWorkspaceAccessSelf(ctx context.Context, client *cli.APIClient) (workspaceAccessSelf, error) {
	var self workspaceAccessSelf
	if err := client.GetJSON(ctx, "/api/workspace-access/self", &self); err != nil {
		return self, err
	}
	if self.PrincipalType != "workspace_access_token" || self.TokenID == "" || self.WorkspaceID == "" {
		return workspaceAccessSelf{}, fmt.Errorf("invalid workspace access identity response")
	}
	if self.Workspace.ID == "" {
		self.Workspace.ID = self.WorkspaceID
	}
	if self.Workspace.ID != self.WorkspaceID {
		return workspaceAccessSelf{}, fmt.Errorf("workspace access identity returned mismatched workspace")
	}
	return self, nil
}

func workspaceSummaryFromAccessSelf(self workspaceAccessSelf) workspaceSummary {
	return workspaceSummary{ID: self.Workspace.ID, Name: self.Workspace.Name, Slug: self.Workspace.Slug}
}
