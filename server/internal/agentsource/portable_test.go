package agentsource

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"testing/fstest"
)

func TestPortableExportRoundTrip(t *testing.T) {
	for _, skills := range [][]Skill{nil, {{SourcePath:"skills/helper", Name:"helper", Description:"Current description", Content:"---\nname: old-name\n---\nHelp", Disabled:true, Files:[]File{{Path:"references/guide.md", Content:"Guide"}}}}} {
		archive, err := ExportSource(context.Background(), "测试 Agent", "Description", "Be helpful", skills)
		if err != nil { t.Fatal(err) }
		reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil { t.Fatal(err) }
		files := fstest.MapFS{}
		for _, file := range reader.File {
			r, err := file.Open(); if err != nil { t.Fatal(err) }
			content, err := io.ReadAll(r); r.Close(); if err != nil { t.Fatal(err) }
			files[file.Name] = &fstest.MapFile{Data:content}
		}
		if files[PortableSchemaPath] == nil { t.Fatal("missing JSON schema") }
		client, err := newFSRepositoryClient(files); if err != nil { t.Fatal(err) }
		snapshot, err := ReadDTARepository(context.Background(), client, Source{})
		if err != nil { t.Fatal(err) }
		bundle := snapshot.Definition
		if bundle.Instructions != "Be helpful" || bundle.Manifest.Metadata.Name != "测试 Agent" || len(bundle.Skills) != len(skills) { t.Fatalf("round trip: %#v", bundle) }
		if len(skills) > 0 && (!bundle.Skills[0].Disabled || bundle.Skills[0].Name != "helper" || bundle.Skills[0].Content != skills[0].Content || bundle.Skills[0].Files[0].Content != "Guide") { t.Fatalf("skill changed: %#v", bundle.Skills[0]) }
		if err := ValidateBundle(bundle); err != nil { t.Fatal(err) }
	}
}

func TestPortableRejectsUnsafeAndUnimportableFiles(t *testing.T) {
	for _, file := range []File{{Path:"../escape", Content:"x"}, {Path:"SKILL.md", Content:"collision"}, {Path:"image.png", Content:"\x00binary"}, {Path:"a/SKILL.md", Content:"nested"}} {
		_, err := ExportSource(context.Background(), "Agent", "", "", []Skill{{SourcePath:"skills/one", Name:"one", Content:"Help", Files:[]File{file}}})
		if err == nil { t.Fatalf("accepted %#v", file) }
	}
}

func TestPortableRejectsUnknownFieldsAndAmbiguousManifests(t *testing.T) {
	manifest := PortableManifest{Schema:PortableSchemaPath, Version:PortableVersion, Name:"Agent", Instructions:"AGENTS.md", Skills:[]PortableSkill{}}
	content, _ := json.Marshal(manifest)
	var raw map[string]any; json.Unmarshal(content, &raw); raw["credentials"] = "secret"
	content, _ = json.Marshal(raw)
	files := fstest.MapFS{PortableManifestPath:{Data:content}, "AGENTS.md":{Data:[]byte("Hi")}}
	client, _ := newFSRepositoryClient(files)
	if _, err := ReadDTARepository(context.Background(), client, Source{}); err == nil { t.Fatal("accepted unknown field") }
	delete(raw, "credentials"); content, _ = json.Marshal(raw); files[PortableManifestPath].Data = content
	files[DTAProjectPath] = &fstest.MapFile{Data:[]byte("{}")}
	client, _ = newFSRepositoryClient(files)
	if _, err := ReadDTARepository(context.Background(), client, Source{}); err == nil { t.Fatal("accepted ambiguous manifests") }
}
