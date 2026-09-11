package handler

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestEveryPortableManifestSectionHasOneImportExportPair(t *testing.T) {
	var schema struct { Defs map[string]struct { Properties map[string]json.RawMessage `json:"properties"` } `json:"$defs"` }
	if err := json.Unmarshal(agentsource.PortableSchema,&schema); err != nil { t.Fatal(err) }
	properties := schema.Defs["v2"].Properties
	delete(properties,"$schema"); delete(properties,"version")
	for _, field := range agentPackageFields {
		if _, exists := properties[field.Name()]; !exists { t.Fatalf("duplicate or unknown codec: %s",field.Name()) }
		delete(properties,field.Name())
	}
	if len(properties) != 0 { t.Fatalf("manifest fields without a paired implementation: %v",properties) }
}

func TestPackageSecretReuseRequiresIdenticalAliasAtEveryDestinationPath(t *testing.T) {
	state := packageBindingState{SecretRefs:map[string]string{"/configuration/custom_env/TOKEN":"auth"}}
	agent := db.Agent{CustomEnv:[]byte(`{"TOKEN":"stored-private-value","OTHER":"not-authorized"}`)}
	for _, test := range []struct{configuration string; reusable bool}{
		{`{"custom_env":{"TOKEN":{"secret_ref":"auth"}}}`,true},
		{`{"custom_env":{"TOKEN":{"secret_ref":"different"}}}`,false},
		{`{"custom_env":{"TOKEN":{"secret_ref":"auth"},"OTHER":{"secret_ref":"auth"}}}`,false},
	} {
		bundle := agentsource.Bundle{Definition:map[string]json.RawMessage{"configuration":json.RawMessage(test.configuration)}}
		values := packageSecretValues(state,bundle,agent)
		if (len(values) > 0) != test.reusable { t.Fatalf("unexpected reuse for %s",test.configuration) }
	}
}

func TestPackageExportTracksManualConfigurationAndKeepsKnownAliases(t *testing.T) {
	declaration := json.RawMessage(`[{"ref":"plugin","enabled":true}]`)
	current := json.RawMessage(`[{"ref":"configured-plugin","enabled":true}]`)
	state := packageBindingState{Receipts:map[string]packageBindingReceipt{"/dsh_plugins":{Declaration:packageValueHash(declaration),Actual:current,Mappings:map[string]string{"plugin":"configured-plugin"}}}}
	changed := json.RawMessage(`[{"ref":"configured-plugin","enabled":false}]`)
	result := state.exportDeclaration("/dsh_plugins",declaration,changed)
	if packageValueHash(result) != packageValueHash(json.RawMessage(`[{"ref":"plugin","enabled":false}]`)) { t.Fatalf("manual plugin edit lost: %s",result) }
	if state.ready("/dsh_plugins",declaration,changed) { t.Fatal("changed selection retained a valid receipt") }
	if !packageBindingMatches(declaration,current,map[string]string{"plugin":"configured-plugin"}) { t.Fatal("valid resource mapping rejected") }
	if packageBindingMatches(declaration,current,map[string]string{"plugin":"another-agent-resource"}) { t.Fatal("foreign resource mapping accepted") }
	if packageBindingMatches(declaration,changed,map[string]string{"plugin":"configured-plugin"}) { t.Fatal("enabled flag mismatch accepted") }
}
