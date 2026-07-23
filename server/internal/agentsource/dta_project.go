package agentsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	DTAProjectPath          = "dingtalk-agent.json"
	DTAProjectSchema        = "dingtalk-agent/project@1"
	DTABasicSkill           = "dingtalk-basic-behavior"
	MaxDTADisplayNameLength = 128
)

var dtaStableName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)

type DTAProject struct {
	Schema     string
	Name       string
	DTAVersion string
	Agent      DTAProjectAgent
}

type DTAProjectAgent struct {
	DisplayName string
	Definition  string
	SkillsRoot  string
	Skills      []string
}

type dtaProjectDocument struct {
	Schema     string          `json:"$schema"`
	Name       string          `json:"name"`
	DTAVersion string          `json:"dtaVersion"`
	Agent      json.RawMessage `json:"agent"`
	Workspaces json.RawMessage `json:"workspaces"`
}

type dtaProjectAgentDocument struct {
	DisplayName json.RawMessage `json:"displayName"`
	Definition  string          `json:"definition"`
	SkillsRoot  string          `json:"skillsRoot"`
	Skills      []string        `json:"skills"`
}

func ParseDTAProject(content []byte) (DTAProject, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	var document dtaProjectDocument
	if err := decoder.Decode(&document); err != nil {
		return DTAProject{}, fmt.Errorf("decode %s: %w", DTAProjectPath, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return DTAProject{}, fmt.Errorf("%s must contain exactly one JSON value", DTAProjectPath)
		}
		return DTAProject{}, fmt.Errorf("decode trailing %s content: %w", DTAProjectPath, err)
	}
	if document.Schema != DTAProjectSchema {
		return DTAProject{}, fmt.Errorf(
			"%s $schema must be %q; unsupported project protocol %q",
			DTAProjectPath,
			DTAProjectSchema,
			document.Schema,
		)
	}
	if !dtaStableName.MatchString(document.Name) {
		return DTAProject{}, fmt.Errorf("%s name must be a stable name of 1 to 64 characters", DTAProjectPath)
	}
	if strings.TrimSpace(document.DTAVersion) == "" {
		return DTAProject{}, fmt.Errorf("%s dtaVersion must be a non-empty string", DTAProjectPath)
	}
	if len(document.Agent) == 0 || bytes.Equal(bytes.TrimSpace(document.Agent), []byte("null")) {
		return DTAProject{}, fmt.Errorf("%s agent must be an object", DTAProjectPath)
	}
	var agentDocument dtaProjectAgentDocument
	if err := json.Unmarshal(document.Agent, &agentDocument); err != nil {
		return DTAProject{}, fmt.Errorf("%s agent must be an object: %w", DTAProjectPath, err)
	}
	displayName := ""
	if len(agentDocument.DisplayName) > 0 {
		if err := json.Unmarshal(agentDocument.DisplayName, &displayName); err != nil ||
			bytes.Equal(bytes.TrimSpace(agentDocument.DisplayName), []byte("null")) {
			return DTAProject{}, errors.New("agent.displayName must be a string when provided")
		}
		displayName = strings.TrimSpace(displayName)
	}
	if err := validateRepositoryPath(agentDocument.Definition, "agent.definition"); err != nil {
		return DTAProject{}, err
	}
	if err := validateRepositoryPath(agentDocument.SkillsRoot, "agent.skillsRoot"); err != nil {
		return DTAProject{}, err
	}
	if len(agentDocument.DisplayName) > 0 &&
		(displayName == "" ||
			utf8.RuneCountInString(displayName) > MaxDTADisplayNameLength ||
			strings.IndexFunc(displayName, func(r rune) bool {
				return r < ' ' || r == '\u007f'
			}) >= 0) {
		return DTAProject{}, fmt.Errorf(
			"agent.displayName must be between 1 and %d characters without control characters",
			MaxDTADisplayNameLength,
		)
	}
	if len(agentDocument.Skills) == 0 {
		return DTAProject{}, errors.New("agent.skills must be a non-empty array")
	}
	if len(agentDocument.Skills) > MaxSkills {
		return DTAProject{}, fmt.Errorf("agent.skills exceeds the %d skill limit", MaxSkills)
	}
	seenSkills := make(map[string]struct{}, len(agentDocument.Skills))
	hasBasicSkill := false
	for _, skillName := range agentDocument.Skills {
		if !dtaStableName.MatchString(skillName) {
			return DTAProject{}, fmt.Errorf("agent.skills contains invalid stable name %q", skillName)
		}
		if _, exists := seenSkills[skillName]; exists {
			return DTAProject{}, fmt.Errorf("agent.skills contains duplicate skill %q", skillName)
		}
		seenSkills[skillName] = struct{}{}
		hasBasicSkill = hasBasicSkill || skillName == DTABasicSkill
	}
	if !hasBasicSkill {
		return DTAProject{}, fmt.Errorf("agent.skills must include %q", DTABasicSkill)
	}
	if len(document.Workspaces) == 0 {
		return DTAProject{}, fmt.Errorf("%s workspaces must be an object", DTAProjectPath)
	}
	var workspaces map[string]json.RawMessage
	if err := json.Unmarshal(document.Workspaces, &workspaces); err != nil || workspaces == nil {
		return DTAProject{}, fmt.Errorf("%s workspaces must be an object", DTAProjectPath)
	}
	return DTAProject{
		Schema:     document.Schema,
		Name:       document.Name,
		DTAVersion: document.DTAVersion,
		Agent: DTAProjectAgent{
			DisplayName: displayName,
			Definition:  agentDocument.Definition,
			SkillsRoot:  agentDocument.SkillsRoot,
			Skills:      append([]string(nil), agentDocument.Skills...),
		},
	}, nil
}

func CompileDTAProject(ctx context.Context, client RepositoryClient, source Source) (Bundle, error) {
	tree, err := client.GetTree(ctx, source.InstallationID, source.Owner, source.Repository, source.CommitSHA)
	if err != nil {
		return Bundle{}, err
	}
	entries := repositoryEntries(tree)
	projectBytes, err := loadRequiredText(ctx, client, source, entries, DTAProjectPath)
	if err != nil {
		return Bundle{}, err
	}
	project, err := ParseDTAProject(projectBytes)
	if err != nil {
		return Bundle{}, fmt.Errorf("compile %s: %w", DTAProjectPath, err)
	}
	displayName := project.Agent.DisplayName
	if displayName == "" {
		displayName = project.Name
	}
	manifest := Manifest{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   ManifestMetadata{Name: displayName},
		Spec: ManifestSpec{
			Instructions:  project.Agent.Definition,
			Skills:        make([]ManifestSkill, 0, len(project.Agent.Skills)),
			Compatibility: ManifestCompatibility{Providers: []string{}},
		},
	}
	expectedSkillNames := make(map[string]string, len(project.Agent.Skills))
	for _, skillName := range project.Agent.Skills {
		skillPath := dtaSkillPath(project.Agent.SkillsRoot, skillName)
		manifest.Spec.Skills = append(manifest.Spec.Skills, ManifestSkill{Path: skillPath})
		expectedSkillNames[skillPath] = skillName
	}
	return compileBundle(
		ctx,
		client,
		source,
		entries,
		len(projectBytes),
		manifest,
		expectedSkillNames,
	)
}

func dtaSkillPath(skillsRoot, skillName string) string {
	return skillsRoot + "/" + skillName
}
