package agentsource

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
)

const sourceContractJSON = `{"version":1,"scope":"产品答疑协调","must_delegate":["所有产品机制结论交执行器查证"],"constraints":["未经确认不得外发"],"clarify_when":["缺少明确产品对象"]}`

func TestCompileSourcesRoundtripCoordinatorContract(t *testing.T) {
	instructions := strings.Repeat("完整业务SOP保留。", 800)
	manifest := `apiVersion: multica.ai/v1alpha1
kind: Agent
metadata: {name: reviewer}
spec:
  instructions: AGENT.md
  coordinator_contract: ` + sourceContractJSON + "\n"
	project := strings.Replace(validDTAProject, `"definition":`, `"coordinatorContract": `+sourceContractJSON+`, "definition":`, 1)
	cases := []struct {
		name string
		fs   fstest.MapFS
		dta  bool
	}{
		{"manifest", fstest.MapFS{
			ManifestPath: {Data: []byte(manifest)},
			"AGENT.md":   {Data: []byte(instructions)},
		}, false},
		{"dta", fstest.MapFS{
			DTAProjectPath:    {Data: []byte(project)},
			"agent/AGENTS.md": {Data: []byte(instructions)},
			"agent/skills/dta-basic-behavior/SKILL.md":          {Data: []byte("---\nname: dta-basic-behavior\n---\nBasic")},
			"agent/skills/multica-development-manager/SKILL.md": {Data: []byte("---\nname: multica-development-manager\n---\nRole")},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compile := CompileFS
			sourcePath := ManifestPath
			if tc.dta {
				compile = CompileDTAProjectFS
				sourcePath = DTAProjectPath
			}
			bundle, err := compile(context.Background(), tc.fs)
			if err != nil {
				t.Fatal(err)
			}
			if bundle.Instructions != instructions {
				t.Fatal("executor instructions were shortened")
			}
			if _, state := coordinatorcontract.Resolve(coordinatorcontract.Marshal(bundle.CoordinatorContract), instructions); state != coordinatorcontract.StateLoaded {
				t.Fatalf("contract state = %s", state)
			}
			encoded, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			var restored Bundle
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			if err := ValidateBundle(restored); err != nil {
				t.Fatalf("restored bundle: %v", err)
			}
			if restored.CoordinatorContract.Scope != "产品答疑协调" || restored.Manifest.Spec.CoordinatorContract.MustDelegate[0] != "所有产品机制结论交执行器查证" {
				t.Fatal("source or snapshot contract was lost")
			}
			broken := restored
			broken.CoordinatorContract = nil
			broken.Hash = hashBundle(broken)
			if err := ValidateBundle(broken); err == nil {
				t.Fatal("missing bound snapshot was accepted")
			}
			broken = restored
			broken.Instructions += "changed"
			broken.Hash = hashBundle(broken)
			if err := ValidateBundle(broken); err == nil {
				t.Fatal("old instructions binding was accepted")
			}
			unknown := strings.Replace(string(encoded), `"scope":"产品答疑协调"`, `"scope":"产品答疑协调","allow_reply":true`, 1)
			if err := json.Unmarshal([]byte(unknown), new(Bundle)); err == nil {
				t.Fatal("unknown persisted contract field was silently discarded")
			}
			tc.fs[sourcePath].Data = []byte(strings.Replace(string(tc.fs[sourcePath].Data), "未经确认不得外发", "禁止任何外发", 1))
			changed, err := compile(context.Background(), tc.fs)
			if err != nil || changed.Hash == bundle.Hash {
				t.Fatalf("contract change must alter bundle hash: %v", err)
			}
		})
	}
}

func TestSourceContractsRejectUnknownFieldsAndOversize(t *testing.T) {
	for _, contract := range []string{
		strings.Replace(sourceContractJSON, `"version":1`, `"version":1,"allow_reply":true`, 1),
		strings.Replace(sourceContractJSON, "产品答疑协调", strings.Repeat("界", 1601), 1),
	} {
		manifest := "apiVersion: multica.ai/v1alpha1\nkind: Agent\nmetadata: {name: reviewer}\nspec:\n  instructions: AGENT.md\n  coordinator_contract: " + contract + "\n"
		if _, err := ParseManifest([]byte(manifest)); err == nil {
			t.Fatal("invalid Git source contract accepted")
		}
		project := strings.Replace(validDTAProject, `"definition":`, `"coordinatorContract": `+contract+`, "definition":`, 1)
		if _, err := ParseDTAProject([]byte(project)); err == nil {
			t.Fatal("invalid DTA source contract accepted")
		}
	}
}

func TestBundleWithoutCoordinatorContractKeepsPublishedHash(t *testing.T) {
	bundle := Bundle{
		Manifest: Manifest{
			APIVersion: APIVersion, Kind: Kind,
			Metadata: ManifestMetadata{Name: "reviewer"},
			Spec:     ManifestSpec{Instructions: "AGENT.md"},
		},
		Instructions: "work",
	}
	// This snapshot hash was produced before coordinator_contract existed.
	const published = "b126086a547dd4a054906d5468f20b585748985d5cbc6fecfdb2873cfb0937b7"
	bundle.Hash = published
	if err := ValidateBundle(bundle); err != nil {
		t.Fatalf("existing snapshot became invalid: %v", err)
	}
}
