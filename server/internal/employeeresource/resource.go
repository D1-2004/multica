// Package employeeresource holds the provider-independent rules for the
// resources of an Employee's source messages: foreground limits, file-name
// and type checks, bounded UTF-8 text extraction and the model-facing
// ResourceContext. It performs no I/O; the Host proves which resources a
// message owns and downloads them before calling into this package.
package employeeresource

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"
)

// ContextVersion is the ResourceContext shape new input snapshots carry.
const ContextVersion = 1

type Relation string

const (
	// Current is an own resource of the outer source message.
	Current Relation = "current"
	// Quoted is an own resource of the message the source exactly quotes.
	Quoted Relation = "quoted"
)

type State string

const (
	Available   State = "available"
	Partial     State = "partial"
	Unavailable State = "unavailable"
	Unsupported State = "unsupported"
)

// Reasons are stable machine codes; the model reads them as facts.
const (
	ReasonTextTruncated       = "text_truncated"
	ReasonTextBudget          = "text_budget_exhausted"
	ReasonTooManyFiles        = "too_many_files"
	ReasonTooManyImages       = "too_many_images"
	ReasonTooLarge            = "too_large"
	ReasonUnsupportedType     = "unsupported_type"
	ReasonTypeMismatch        = "type_mismatch"
	ReasonInvalidEncoding     = "invalid_encoding"
	ReasonCorruptContent      = "corrupt_content"
	ReasonInvalidName         = "invalid_name"
	ReasonVisionUnavailable   = "vision_unavailable"
	ReasonProviderUnavailable = "provider_unavailable"
	ReasonQuoteUnverified     = "quote_unverified"
	ReasonRequesterMismatch   = "requester_mismatch"
)

// Limits bound one foreground read. The defaults are the D1 starting values.
type Limits struct {
	MaxFilesPerMessage  int
	MaxImagesPerMessage int
	// MaxDownloadsPerRead bounds downloads across a whole window.
	MaxDownloadsPerRead int
	MaxFileBytes        int64
	// MaxTextBytes bounds extracted text across a whole window.
	MaxTextBytes int
}

func DefaultLimits() Limits {
	return Limits{MaxFilesPerMessage: 4, MaxImagesPerMessage: 2, MaxDownloadsPerRead: 4, MaxFileBytes: 10 << 20, MaxTextBytes: 16 << 10}
}

// Range is the byte range of the decoded text that Text holds.
type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
	Total int `json:"total"`
}

// Item is one resource of the model-facing context. It carries no provider
// resource IDs, URLs, credentials or local paths: Ref names the Host's frozen
// record, which alone binds the read to its source and authority.
type Item struct {
	Ref       string   `json:"resource_ref,omitempty"`
	SourceRef string   `json:"source_ref"`
	Relation  Relation `json:"relation"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name,omitempty"`
	MediaType string   `json:"media_type,omitempty"`
	State     State    `json:"state"`
	Reason    string   `json:"reason,omitempty"`
	SizeBytes int64    `json:"size_bytes,omitempty"`
	SHA256    string   `json:"sha256,omitempty"`
	Text      string   `json:"text,omitempty"`
	Range     *Range   `json:"range,omitempty"`
	// Retryable marks a transient provider failure; it was not frozen.
	Retryable bool `json:"retryable,omitempty"`
}

// Context is the bounded ResourceContext of one wake. Resource text is
// untrusted user data: it never grants authority or changes instructions.
type Context struct {
	Version   int    `json:"version"`
	Items     []Item `json:"items"`
	TextBytes int    `json:"text_bytes"`
}

// Kind classifies a provider resource. Only the provider's structured
// resourceType is consulted.
func Kind(resourceType string) string {
	switch strings.ToLower(strings.TrimSpace(resourceType)) {
	case "file":
		return "file"
	case "image", "picture", "photo":
		return "image"
	case "video":
		return "video"
	case "voice", "audio":
		return "audio"
	default:
		return "other"
	}
}

// Verdict is the foreground decision for one own resource.
type Verdict struct {
	Fetch  bool
	State  State
	Reason string
}

// Plan decides, in provider order, which own resources of one message may be
// fetched in the foreground. Nothing over a limit is dropped silently: it
// stays listed with an explicit state. downloadsLeft is the window's budget;
// used is how much of it the message consumed.
func Plan(kinds []string, limits Limits, downloadsLeft int) (verdicts []Verdict, used int) {
	files, images := 0, 0
	verdicts = make([]Verdict, len(kinds))
	for i, kind := range kinds {
		switch kind {
		case "file":
			files++
			switch {
			case files > limits.MaxFilesPerMessage || downloadsLeft-used <= 0:
				verdicts[i] = Verdict{State: Unavailable, Reason: ReasonTooManyFiles}
			default:
				verdicts[i] = Verdict{Fetch: true}
				used++
			}
		case "image":
			images++
			if images > limits.MaxImagesPerMessage {
				verdicts[i] = Verdict{State: Unavailable, Reason: ReasonTooManyImages}
			} else {
				// No vision path is configured for the foreground; say so
				// instead of letting the model guess at pixels it never saw.
				verdicts[i] = Verdict{State: Unsupported, Reason: ReasonVisionUnavailable}
			}
		default:
			verdicts[i] = Verdict{State: Unsupported, Reason: ReasonUnsupportedType}
		}
	}
	return verdicts, used
}

// Extraction is the checked, bounded text of one downloaded file.
type Extraction struct {
	State     State
	Reason    string
	Name      string
	MediaType string
	SizeBytes int64
	SHA256    string
	Text      string
	Range     *Range
}

var textTypes = map[string]string{".txt": "text/plain", ".md": "text/markdown", ".csv": "text/csv", ".json": "application/json"}

// Declared types a provider may legitimately attach to a text file.
var compatibleDeclaredTypes = map[string]bool{
	"text/plain": true, "text/markdown": true, "text/x-markdown": true, "text/csv": true, "text/comma-separated-values": true,
	"application/csv": true, "application/json": true, "text/json": true,
	"application/octet-stream": true, "binary/octet-stream": true, "application/force-download": true, "application/download": true, "application/x-download": true,
}

// Extract validates one downloaded file and returns at most budget bytes of
// its UTF-8 text, never splitting a character. The hash covers the whole
// downloaded content so a replay can prove it reads the same bytes.
func Extract(name, declaredType string, data []byte, budget int, limits Limits) Extraction {
	sum := sha256.Sum256(data)
	out := Extraction{Name: strings.TrimSpace(name), SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	reject := func(state State, reason string) Extraction {
		out.State, out.Reason = state, reason
		return out
	}
	if int64(len(data)) > limits.MaxFileBytes {
		return reject(Unavailable, ReasonTooLarge)
	}
	if !ValidName(out.Name) {
		out.Name = ""
		return reject(Unavailable, ReasonInvalidName)
	}
	mediaType, ok := textTypes[strings.ToLower(path.Ext(out.Name))]
	if !ok {
		return reject(Unsupported, ReasonUnsupportedType)
	}
	out.MediaType = mediaType
	if declared := strings.TrimSpace(declaredType); declared != "" {
		parsed, _, err := mime.ParseMediaType(declared)
		if err != nil || !compatibleDeclaredTypes[strings.ToLower(parsed)] {
			return reject(Unavailable, ReasonTypeMismatch)
		}
	}
	if sniffed := http.DetectContentType(data); len(data) > 0 && !strings.HasPrefix(sniffed, "text/plain") &&
		!(mediaType == "text/markdown" && strings.HasPrefix(sniffed, "text/html")) {
		// Magic bytes of an image, archive, PDF or executable under a text
		// extension. Markdown may legitimately open with inline HTML.
		return reject(Unavailable, ReasonTypeMismatch)
	}
	text := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(text) || bytes.IndexByte(text, 0) >= 0 {
		return reject(Unavailable, ReasonInvalidEncoding)
	}
	if mediaType == "application/json" && !json.Valid(text) {
		return reject(Unavailable, ReasonCorruptContent)
	}
	total := len(text)
	end := TruncateUTF8(string(text), max(budget, 0))
	out.Text, out.Range = string(text[:end]), &Range{Start: 0, End: end, Total: total}
	switch {
	case end == total:
		out.State = Available
	case end == 0:
		out.State, out.Reason = Partial, ReasonTextBudget
	default:
		out.State, out.Reason = Partial, ReasonTextTruncated
	}
	return out
}

// TruncateUTF8 returns the largest prefix length of s that is at most limit
// bytes and ends on a character boundary.
func TruncateUTF8(s string, limit int) int {
	if len(s) <= limit {
		return len(s)
	}
	n := max(limit, 0)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// ValidName accepts a plain file name: no directory part, traversal, control
// or bidirectional-override characters that could disguise its extension.
func ValidName(name string) bool {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) || name == "." || name == ".." || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		switch {
		case r < 0x20, r == 0x7f, r == '/', r == '\\', r == ':':
			return false
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f:
			return false
		}
	}
	return true
}
