package dshprofile

import (
	"encoding/json"
	"errors"
	"regexp"
)

type NativePlugin struct {
	PackageName string        `json:"package_name"`
	Version     string        `json:"version"`
	Rows        []string      `json:"rows"`
	Patches     []RowOverride `json:"patches"`
}

type NativeSnapshot struct {
	Version      int            `json:"version"`
	WorkspaceID  string         `json:"workspace_id"`
	AgentID      string         `json:"agent_id"`
	BaseRevision string         `json:"base_revision"`
	Plugins      []NativePlugin `json:"plugins"`
	Fingerprint  string         `json:"fingerprint"`
}

var nativeRevisionPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

func ValidateRowOverrides(rows []RowOverride, owned []string) error {
	if len(rows) > 4096 {
		return errors.New("too many plugin configuration rows")
	}
	allowed := map[string]bool{}
	for _, id := range owned {
		allowed[id] = true
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if !rowPattern.MatchString(row.ID) || seen[row.ID] || (owned != nil && !allowed[row.ID]) {
			return errors.New("plugin configuration names an undeclared or duplicate row")
		}
		seen[row.ID] = true
		if len(row.Config) > 60000 || (len(row.Config) > 0 && !json.Valid(row.Config)) {
			return errors.New("invalid plugin row configuration")
		}
	}
	return nil
}

func (s NativeSnapshot) Validate(workspace, agent string) error {
	invalid := errors.New("invalid native plugin snapshot")
	if s.Version != 1 || s.WorkspaceID != workspace || s.AgentID != agent || !nativeRevisionPattern.MatchString(s.BaseRevision) || !digestPattern.MatchString(s.Fingerprint) || s.Plugins == nil || len(s.Plugins) > 128 {
		return invalid
	}
	seen := map[string]bool{}
	for _, plugin := range s.Plugins {
		if !packagePattern.MatchString(plugin.PackageName) || len(plugin.PackageName) > 214 || !versionPattern.MatchString(plugin.Version) || seen[plugin.PackageName] || len(plugin.Rows) == 0 {
			return invalid
		}
		seen[plugin.PackageName] = true
		if err := ValidateRowOverrides(plugin.Patches, plugin.Rows); err != nil {
			return invalid
		}
	}
	return nil
}
