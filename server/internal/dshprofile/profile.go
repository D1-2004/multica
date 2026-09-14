// Package dshprofile owns durable employee Profile revisions and Host receipts.
package dshprofile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrPending = errors.New("DSH employee Profile is awaiting immutable builds or Host application")
var ErrChanged = errors.New("DSH employee Profile changed")
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var packagePattern = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
var rowPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

type SourcePlugin struct {
	ID             uuid.UUID      `json:"id"`
	Enabled        bool           `json:"enabled"`
	ConfigRevision int64          `json:"config_revision"`
	PackageName    string         `json:"package_name"`
	Version        string         `json:"version"`
	Integrity      string         `json:"integrity"`
	SourceKind     string         `json:"source_kind"`
	SourceSpec     string         `json:"source_spec"`
	ArtifactKey    string         `json:"artifact_key"`
	RowID          string         `json:"row_id"`
	Config         map[string]any `json:"config"`
}

type Source struct {
	TemplateID string         `json:"template_id"`
	Plugins    []SourcePlugin `json:"plugins"`
}

// ReadSource runs on the same transaction that serializes Profile publication.
// Configuration values never cross the public status API.
type ReadSource func(context.Context, *db.Queries, dshhost.Key, string) (Source, error)

type Plugin struct {
	PackageName string         `json:"package_name"`
	Version     string         `json:"version"`
	Integrity   string         `json:"integrity"`
	BuildDigest string         `json:"build_digest"`
	RowID       string         `json:"row_id"`
	Config      map[string]any `json:"config"`
}

type Descriptor struct {
	Version     int       `json:"version"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	AgentID     uuid.UUID `json:"agent_id"`
	Revision    string    `json:"revision"`
	Plugins     []Plugin  `json:"plugins"`
}

type Build struct {
	ID          uuid.UUID
	Key         string
	State       string
	Digest      string
	ArtifactKey string
}

type Revision struct {
	ID           int64
	SourceDigest string
	TemplateID   string
	Descriptor   string
	Digest       string
	Builds       []Build
}

func hash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func EncodeSource(source Source) (string, string, error) {
	invalid := errors.New("invalid employee Profile source")
	if source.TemplateID == "" || len(source.Plugins) > 128 {
		return "", "", invalid
	}
	source.Plugins = append([]SourcePlugin{}, source.Plugins...)
	sort.Slice(source.Plugins, func(i, j int) bool { return source.Plugins[i].PackageName < source.Plugins[j].PackageName })
	seen := map[string]bool{}
	for _, plugin := range source.Plugins {
		if plugin.ID == uuid.Nil || seen[plugin.PackageName] || len(plugin.PackageName) > 214 || !packagePattern.MatchString(plugin.PackageName) || !versionPattern.MatchString(plugin.Version) || len(plugin.Integrity) != 71 || plugin.Integrity[:7] != "sha256-" || !digestPattern.MatchString(plugin.Integrity[7:]) || plugin.Config == nil || plugin.ConfigRevision < 0 || len(plugin.RowID) > 160 || (len(plugin.Config) > 0 && !rowPattern.MatchString(plugin.RowID)) {
			return "", "", invalid
		}
		seen[plugin.PackageName] = true
	}
	raw, err := json.Marshal(source)
	if err != nil || len(raw) > 1024*1024 {
		return "", "", invalid
	}
	return string(raw), hash(raw), nil
}

func BuildKey(template string, plugin SourcePlugin) string {
	// Credentials and employee-specific settings do not affect package builds.
	raw, _ := json.Marshal([]string{template, plugin.ID.String(), plugin.PackageName, plugin.Version, plugin.Integrity, plugin.SourceKind, plugin.SourceSpec, plugin.ArtifactKey})
	return hash(raw)
}

func Resolve(key dshhost.Key, revision int64, source Source, builds map[string]Build) (string, string, error) {
	if key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || revision < 1 {
		return "", "", errors.New("invalid employee Profile identity")
	}
	if _, _, err := EncodeSource(source); err != nil {
		return "", "", err
	}
	descriptor := Descriptor{Version: 1, WorkspaceID: key.WorkspaceID, AgentID: key.AgentID, Revision: strconv.FormatInt(revision, 10), Plugins: []Plugin{}}
	for _, plugin := range source.Plugins {
		if !plugin.Enabled {
			continue
		}
		build, ok := builds[BuildKey(source.TemplateID, plugin)]
		if !ok || build.State != "ready" || !digestPattern.MatchString(build.Digest) || build.ArtifactKey == "" {
			return "", "", ErrPending
		}
		descriptor.Plugins = append(descriptor.Plugins, Plugin{PackageName: plugin.PackageName, Version: plugin.Version, Integrity: plugin.Integrity, BuildDigest: build.Digest, RowID: plugin.RowID, Config: plugin.Config})
	}
	sort.Slice(descriptor.Plugins, func(i, j int) bool { return descriptor.Plugins[i].PackageName < descriptor.Plugins[j].PackageName })
	raw, err := json.Marshal(descriptor)
	if err != nil || len(raw) > 1024*1024 {
		return "", "", errors.New("invalid employee Profile descriptor")
	}
	return string(raw), hash(raw), nil
}
