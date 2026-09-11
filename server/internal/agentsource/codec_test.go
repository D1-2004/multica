package agentsource

import (
	"encoding/json"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"strings"
	"testing"
)

type codecFixture struct{ Value string }
type instructionFixtureCodec struct{}
func (instructionFixtureCodec) Import(c *codecFixture, v string) error { c.Value = v; return nil }
func (instructionFixtureCodec) Export(c *codecFixture) (string, error) { return c.Value, nil }

func TestPackageCodecUsesOneTypedPairForBothDirections(t *testing.T) {
	field := NewPackageField("instructions", instructionFixtureCodec{})
	state := &codecFixture{}
	if err := field.Import(state, json.RawMessage(`"hello"`)); err != nil { t.Fatal(err) }
	data, err := field.Export(state)
	if err != nil || string(data) != `"hello"` { t.Fatalf("round trip: %s %v", data, err) }
	if err := field.Import(state, json.RawMessage(`123`)); err == nil || !strings.Contains(err.Error(), "instructions") { t.Fatalf("wrong type accepted: %v", err) }
}

func TestPackageCodecRejectsUnpairedImplementationsAtCompileTime(t *testing.T) {
	contract, err := os.ReadFile("codec.go")
	if err != nil { t.Fatal(err) }
	for _, test := range []struct{name, methods string; valid bool}{
		{"paired", `func (fixture) Import(*state,string) error { return nil }; func (fixture) Export(*state)(string,error) { return "",nil }`,true},
		{"missing_export", `func (fixture) Import(*state,string) error { return nil }`,false},
		{"different_value_type", `func (fixture) Import(*state,string) error { return nil }; func (fixture) Export(*state)(int,error) { return 0,nil }`,false},
	} {
		t.Run(test.name,func(t *testing.T) {
			fs := token.NewFileSet()
			production, err := parser.ParseFile(fs,"codec.go",contract,0); if err != nil { t.Fatal(err) }
			fixture, err := parser.ParseFile(fs,"fixture.go",`package agentsource; type state struct{}; type fixture struct{}; `+test.methods+`; var registered = NewPackageField[state,string]("instructions",fixture{})`,0); if err != nil { t.Fatal(err) }
			_, err = (&types.Config{Importer:importer.Default()}).Check("codecfixture",fs,[]*ast.File{production,fixture},nil)
			if test.valid && err != nil { t.Fatal(err) }
			if !test.valid && (err == nil || !strings.Contains(err.Error(),"Export")) { t.Fatalf("expected compile error for Export: %v",err) }
		})
	}
}
