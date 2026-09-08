package agentsource

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ManifestIssue identifies a schema rule without echoing secret-bearing values.
type ManifestIssue struct {
	Path string `json:"path"`
	Keyword string `json:"keyword"`
}

type ManifestSchemaError struct {
	Issues []ManifestIssue `json:"issues"`
}

func (e *ManifestSchemaError) Error() string {
	if len(e.Issues) == 0 { return "manifest schema validation failed" }
	return fmt.Sprintf("manifest schema validation failed at %s (%s)", e.Issues[0].Path, e.Issues[0].Keyword)
}

var manifestSchemas = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) { return nil, errors.New("external manifest schemas are disabled") }
	const schemaURL = "https://multica.invalid/agent.schema.json"
	if err := compiler.AddResource(schemaURL, bytes.NewReader(PortableSchema)); err != nil { return nil, err }
	result := map[string]*jsonschema.Schema{}
	for version, fragment := range map[string]string{"multica.agent/v1":"v1", "multica.agent/v2":"v2", "":""} {
		location := schemaURL
		if fragment != "" { location += "#/$defs/" + fragment }
		schema, err := compiler.Compile(location)
		if err != nil { return nil, err }
		result[version] = schema
	}
	return result, nil
})

// ValidateManifestJSON uses only the embedded contract. A package's schema file
// is an editor aid and never supplies validation rules or remote references.
func ValidateManifestJSON(content []byte) (map[string]json.RawMessage, error) {
	if len(content) > MaxFileSize || !isText(content) { return nil, errors.New("manifest must be UTF-8 text within the file size limit") }
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil { return nil, errors.New("manifest must contain valid JSON") }
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) { return nil, errors.New("manifest must contain exactly one JSON object") }
	schemas, err := manifestSchemas()
	if err != nil { return nil, fmt.Errorf("compile embedded manifest schema: %w", err) }
	schema := schemas[""]
	if object, ok := value.(map[string]any); ok {
		if version, ok := object["version"].(string); ok && schemas[version] != nil { schema = schemas[version] }
	}
	if err := schema.Validate(value); err != nil {
		var invalid *jsonschema.ValidationError
		if !errors.As(err, &invalid) { return nil, errors.New("manifest schema validation failed") }
		issues := make([]ManifestIssue, 0)
		var collect func(*jsonschema.ValidationError)
		collect = func(problem *jsonschema.ValidationError) {
			if len(issues) >= 20 { return }
			if len(problem.Causes) > 0 { for _, cause := range problem.Causes { collect(cause) }; return }
			location := problem.InstanceLocation
			if location == "" { location = "/" }
			keyword := problem.KeywordLocation[strings.LastIndex(problem.KeywordLocation, "/")+1:]
			issues = append(issues, ManifestIssue{Path:location, Keyword:keyword})
		}
		collect(invalid)
		return nil, &ManifestSchemaError{Issues:issues}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil { return nil, errors.New("manifest must be an object") }
	return fields, nil
}
