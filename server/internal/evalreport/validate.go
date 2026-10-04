package evalreport

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed report-contract.json
var contractJSON []byte

// Contract returns the embedded OpenAPI contract without exposing mutable shared bytes.
func Contract() []byte { return bytes.Clone(contractJSON) }

var submissionSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external evaluation schemas are disabled")
	}
	const schemaURL = "https://multica.invalid/eval-report-contract.json"
	if err := compiler.AddResource(schemaURL, bytes.NewReader(contractJSON)); err != nil {
		return nil, err
	}
	return compiler.Compile(schemaURL + "#/components/schemas/Submission")
})

func invalid(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }

// Decode applies the embedded schema and semantic checks. Unknown and duplicate
// fields are rejected. Errors never include submitted values or schema excerpts.
func Decode(data []byte) (Submission, error) {
	var in Submission
	if len(data) > MaxSubmissionBytes || !utf8.Valid(data) {
		return in, invalid("body", "requires UTF-8 JSON within 2 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readJSONValue(decoder, 0)
	if err != nil {
		return in, invalid("body", "requires one JSON object without duplicate fields")
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return in, invalid("body", "requires exactly one JSON object")
	}
	schema, err := submissionSchema()
	if err != nil {
		return in, ErrUnavailable
	}
	if err := schema.Validate(value); err != nil {
		return in, invalid("body", "does not match the schema_version 1 contract")
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return Submission{}, invalid("body", "contains an invalid typed field")
	}
	if err := validateSemantics(in); err != nil {
		return Submission{}, err
	}
	return in, nil
}

// readJSONValue rejects duplicate names (including escaped aliases) rather than
// silently selecting a last value; nesting is bounded independently of body size.
func readJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrInvalid
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delim {
	case '{':
		value := map[string]any{}
		for decoder.More() {
			name, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := name.(string)
			if !ok {
				return nil, ErrInvalid
			}
			if _, exists := value[key]; exists {
				return nil, ErrInvalid
			}
			value[key], err = readJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrInvalid
		}
		return value, nil
	case '[':
		value := []any{}
		for decoder.More() {
			item, err := readJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			value = append(value, item)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrInvalid
		}
		return value, nil
	default:
		return nil, ErrInvalid
	}
}

// Validate also protects non-HTTP callers with the exact same contract as Decode.
func Validate(in Submission) error {
	data, err := json.Marshal(in)
	if err != nil {
		return invalid("body", "cannot encode report")
	}
	_, err = Decode(data)
	return err
}

func validateSemantics(in Submission) error {
	if in.FinishedAt.Before(in.StartedAt) {
		return invalid("finished_at", "cannot precede started_at")
	}
	selected := make(map[string]bool, len(in.SelectedCases))
	for _, definition := range in.SelectedCases {
		if selected[definition.ID] {
			return invalid("selected_cases", "requires unique case IDs")
		}
		selected[definition.ID] = true
	}
	if len(in.Results) != len(selected) {
		return invalid("results", "requires exactly one result for every selected case")
	}
	seen := make(map[string]bool, len(in.Results))
	for _, result := range in.Results {
		if !selected[result.CaseID] || seen[result.CaseID] {
			return invalid("results", "requires exactly one result for every selected case")
		}
		seen[result.CaseID] = true
		for _, evidence := range result.Evidence {
			if !validReference(evidence.Reference) {
				return invalid("results.evidence.reference", "requires credential-free HTTPS or a redacted relative path")
			}
		}
	}
	data, _ := json.Marshal(in)
	var fields any
	_ = json.Unmarshal(data, &fields)
	if containsSensitive(fields) {
		return invalid("body", "credentials or signed references are not permitted")
	}
	return nil
}

var (
	jwtPattern        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	credentialPattern = regexp.MustCompile(`(?i)\b(?:access[_-]?token|refresh[_-]?token|api[_-]?key|client[_-]?secret|password|authorization)\s*[:=]\s*["']?([^\s"'<>]+)`)
	bearerPattern     = regexp.MustCompile(`(?i)\bbearer\s+([a-z0-9._~+/=-]{16,})`)
	signedPattern     = regexp.MustCompile(`(?i)(?:[?&]|\b)(?:x-amz-[a-z-]+|x-oss-[a-z-]+|ossaccesskeyid|signature|securitytoken|access_token|refresh_token|token|api_key|apikey|credential|secret|auth|authorization|password)\s*=`)
	urlPattern        = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
)

func redacted(value string) bool {
	value = strings.Trim(strings.ToLower(value), "[]{}()")
	return value == "redacted" || value == "masked" || value == "placeholder" || strings.Trim(value, "*") == ""
}

func sensitiveText(text string) bool {
	if jwtPattern.MatchString(text) || strings.Contains(text, "-----BEGIN PRIVATE KEY-----") || strings.Contains(text, "-----BEGIN RSA PRIVATE KEY-----") || signedPattern.MatchString(text) {
		return true
	}
	for _, match := range credentialPattern.FindAllStringSubmatch(text, -1) {
		if !redacted(match[1]) {
			return true
		}
	}
	for _, match := range bearerPattern.FindAllStringSubmatch(text, -1) {
		if !redacted(match[1]) {
			return true
		}
	}
	for _, reference := range urlPattern.FindAllString(text, -1) {
		parsed, err := url.Parse(reference)
		if err != nil {
			continue
		}
		if parsed.User != nil {
			return true
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return true
		}
		for key := range query {
			if credentialQueryKey(key) {
				return true
			}
		}
	}
	return false
}

func containsSensitive(value any) bool {
	switch value := value.(type) {
	case string:
		return sensitiveText(value)
	case []any:
		for _, item := range value {
			if containsSensitive(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range value {
			if containsSensitive(item) {
				return true
			}
		}
	}
	return false
}

func validReference(reference string) bool {
	if strings.TrimSpace(reference) != reference || strings.ContainsAny(reference, "\\\x00") || strings.IndexFunc(reference, unicode.IsControl) >= 0 || sensitiveText(reference) {
		return false
	}
	parsed, err := url.Parse(reference)
	if err != nil {
		return false
	}
	if parsed.IsAbs() {
		if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
			return false
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return false
		}
		for key := range query {
			if credentialQueryKey(key) {
				return false
			}
		}
		return !sensitiveText(parsed.Fragment) && !sensitiveText(parsed.Path)
	}
	if parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.HasPrefix(reference, "/") || strings.HasPrefix(reference, "~") || strings.Contains(reference, ":") {
		return false
	}
	decoded := parsed.Path
	if decoded == "" || strings.ContainsAny(decoded, "\\\x00") || strings.IndexFunc(decoded, unicode.IsControl) >= 0 || strings.HasPrefix(decoded, "/") || strings.HasPrefix(decoded, "~") {
		return false
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return path.Clean(decoded) == decoded && !sensitiveText(decoded)
}

func credentialQueryKey(key string) bool {
	key = strings.ToLower(key)
	compact := strings.NewReplacer("-", "", "_", "").Replace(key)
	if strings.HasPrefix(compact, "xamz") || strings.HasPrefix(compact, "xoss") || strings.HasPrefix(compact, "xgoog") {
		return true
	}
	switch compact {
	case "sig", "signature", "ossaccesskeyid", "accesskeyid", "accesskeysecret", "securitytoken", "accesstoken", "refreshtoken", "token", "apikey", "credential", "credentials", "secret", "auth", "authorization", "password", "jwt", "key":
		return true
	}
	return false
}

// IsHTTPSReference identifies references safe to render as external links. Relative
// evidence references remain plain text, never downloads or local file links.
func IsHTTPSReference(reference string) bool {
	return strings.HasPrefix(reference, "https://") && validReference(reference)
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Normalize returns an independent canonical snapshot and server-derived totals.
// It does not consult the published catalog, fetch evidence or execute cases.
func Normalize(in Submission) (Submission, Summary, string, string, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return Submission{}, Summary{}, "", "", invalid("body", "cannot encode report")
	}
	normalized, err := Decode(data)
	if err != nil {
		return Submission{}, Summary{}, "", "", err
	}
	normalized.RunID = strings.ToLower(normalized.RunID)
	normalized.TargetRevision = strings.ToLower(normalized.TargetRevision)
	normalized.Catalog.Revision = strings.ToLower(normalized.Catalog.Revision)
	normalized.Catalog.SHA256 = strings.ToLower(normalized.Catalog.SHA256)
	normalized.StartedAt = normalized.StartedAt.UTC()
	normalized.FinishedAt = normalized.FinishedAt.UTC()
	sort.Slice(normalized.SelectedCases, func(i, j int) bool { return normalized.SelectedCases[i].ID < normalized.SelectedCases[j].ID })
	sort.Slice(normalized.Results, func(i, j int) bool { return normalized.Results[i].CaseID < normalized.Results[j].CaseID })
	definition, _ := json.Marshal(normalized.SelectedCases)
	content, _ := json.Marshal(normalized)
	summary := Summary{Total: len(normalized.SelectedCases), P0Total: 20, Conclusion: "pass"}
	for _, definition := range normalized.SelectedCases {
		if definition.Kind == "p0" {
			summary.P0Selected++
		}
	}
	for _, result := range normalized.Results {
		switch result.Status {
		case "pass":
			summary.Pass++
		case "fail":
			summary.Fail++
		case "blocked":
			summary.Blocked++
		case "incomplete":
			summary.Incomplete++
		case "skipped":
			summary.Skipped++
		}
	}
	summary.PassRate = float64(summary.Pass) / float64(summary.Total)
	if summary.Fail > 0 {
		summary.Conclusion = "fail"
	} else if summary.Pass < summary.Total {
		summary.Conclusion = "incomplete"
	}
	if normalized.ExecutionKind == "real_e2e" {
		summary.RealE2EPass = summary.Pass
	}
	return normalized, summary, digest(content), digest(definition), nil
}
