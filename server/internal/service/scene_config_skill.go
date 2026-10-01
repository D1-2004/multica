package service

import (
	"embed"
	"io/fs"
	"path"
	"sort"
)

// SceneConfigSkillName is the dynamic skill of a task with a current Agent
// work scene (a group or a 1:1 chat): it teaches the config-qwen-tag-scene
// MCP tools. It is not a built-in skill: the claim adds it only to such a
// task, next to the server of the same name.
const SceneConfigSkillName = "config-qwen-tag-scene"

//go:embed scene_config_skill/config-qwen-tag-scene
var sceneConfigSkillFS embed.FS

var sceneConfigSkill = loadSceneConfigSkill()

func loadSceneConfigSkill() AgentSkillData {
	root := "scene_config_skill/" + SceneConfigSkillName
	content, err := sceneConfigSkillFS.ReadFile(root + "/SKILL.md")
	if err != nil {
		panic("scene config skill: " + err.Error())
	}
	skill := AgentSkillData{
		Name:        SceneConfigSkillName,
		Description: "Read and change the current DingTalk scene's configuration (prompts, routines, offered skills and connectors, remote MCP servers) through the config-qwen-tag-scene MCP tools.",
		Content:     string(content),
	}
	var files []string
	_ = fs.WalkDir(sceneConfigSkillFS, root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && path.Base(p) != "SKILL.md" {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	for _, p := range files {
		data, err := sceneConfigSkillFS.ReadFile(p)
		if err != nil {
			panic("scene config skill: " + err.Error())
		}
		skill.Files = append(skill.Files, AgentSkillFileData{Path: p[len(root)+1:], Content: string(data)})
	}
	return skill
}

// SceneConfigSkill returns the config-qwen-tag-scene skill.
func SceneConfigSkill() AgentSkillData {
	out := sceneConfigSkill
	out.Files = append([]AgentSkillFileData(nil), sceneConfigSkill.Files...)
	return out
}

// WithSceneConfigSkill adds the config-qwen-tag-scene skill to skills,
// dropping a workspace skill of the same name so the two never collide on
// disk. The claim and the bundle resolution both build the list through it.
func WithSceneConfigSkill(skills []AgentSkillData) []AgentSkillData {
	out := make([]AgentSkillData, 0, len(skills)+1)
	for _, skill := range skills {
		if skill.Name == SceneConfigSkillName {
			continue
		}
		out = append(out, skill)
	}
	return append(out, SceneConfigSkill())
}
