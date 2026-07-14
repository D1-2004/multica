package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const GitAgentTemplatesEnv = "MULTICA_GIT_AGENT_TEMPLATES_JSON"

var gitAgentTemplateKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

var ErrGitAgentTemplateNotFound = errors.New("git agent template not found")

// GitAgentTemplate is a catalog entry for a repository-backed Agent template.
// It contains discovery metadata only; executable Agent content continues to
// come from multica-agent.yaml and the pinned Git commit.
type GitAgentTemplate struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Repository  string `json:"repository"`
	Ref         string `json:"ref"`
	Enabled     bool   `json:"enabled"`
}

// GitAgentTemplateCatalog deliberately hides the backing store. The MVP uses
// immutable deployment configuration; a future workspace database provider can
// implement the same contract without changing handlers or the CLI.
type GitAgentTemplateCatalog interface {
	List(ctx context.Context, workspaceID string) []GitAgentTemplate
	Get(ctx context.Context, workspaceID, key string) (GitAgentTemplate, error)
}

type StaticGitAgentTemplateCatalog struct {
	items []GitAgentTemplate
	byKey map[string]GitAgentTemplate
}

func NewStaticGitAgentTemplateCatalog(raw string) (*StaticGitAgentTemplateCatalog, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &StaticGitAgentTemplateCatalog{byKey: map[string]GitAgentTemplate{}}, nil
	}

	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var items []GitAgentTemplate
	if err := decoder.Decode(&items); err != nil {
		return nil, fmt.Errorf("parse %s: %w", GitAgentTemplatesEnv, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: trailing JSON content", GitAgentTemplatesEnv)
	}

	byKey := make(map[string]GitAgentTemplate, len(items))
	for i := range items {
		item := &items[i]
		item.Key = strings.TrimSpace(item.Key)
		item.DisplayName = strings.TrimSpace(item.DisplayName)
		item.Description = strings.TrimSpace(item.Description)
		item.Repository = strings.TrimSpace(item.Repository)
		item.Ref = strings.TrimSpace(item.Ref)
		if err := validateGitAgentTemplate(*item); err != nil {
			return nil, fmt.Errorf("%s item %d: %w", GitAgentTemplatesEnv, i, err)
		}
		if _, exists := byKey[item.Key]; exists {
			return nil, fmt.Errorf("%s item %d: duplicate key %q", GitAgentTemplatesEnv, i, item.Key)
		}
		byKey[item.Key] = *item
	}

	// Config order is not a stable API contract. Sort by key so multiple server
	// replicas return byte-for-byte deterministic list results.
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return &StaticGitAgentTemplateCatalog{items: items, byKey: byKey}, nil
}

func validateGitAgentTemplate(item GitAgentTemplate) error {
	if !gitAgentTemplateKeyPattern.MatchString(item.Key) {
		return errors.New("key must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	if item.DisplayName == "" {
		return errors.New("display_name is required")
	}
	if len(item.DisplayName) > 128 {
		return errors.New("display_name must be 128 bytes or fewer")
	}
	if len(item.Description) > 512 {
		return errors.New("description must be 512 bytes or fewer")
	}
	owner, repo, ok := strings.Cut(item.Repository, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") || strings.ContainsAny(item.Repository, " \t\r\n\x00") {
		return errors.New("repository must be owner/repository")
	}
	if item.Ref == "" || len(item.Ref) > 255 || strings.ContainsRune(item.Ref, '\x00') {
		return errors.New("ref is required and must be 255 bytes or fewer")
	}
	return nil
}

func (c *StaticGitAgentTemplateCatalog) List(_ context.Context, _ string) []GitAgentTemplate {
	if c == nil {
		return nil
	}
	return append([]GitAgentTemplate(nil), c.items...)
}

func (c *StaticGitAgentTemplateCatalog) Get(_ context.Context, _ string, key string) (GitAgentTemplate, error) {
	if c == nil {
		return GitAgentTemplate{}, ErrGitAgentTemplateNotFound
	}
	item, ok := c.byKey[strings.TrimSpace(key)]
	if !ok {
		return GitAgentTemplate{}, ErrGitAgentTemplateNotFound
	}
	return item, nil
}
