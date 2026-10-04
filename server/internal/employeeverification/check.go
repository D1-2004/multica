package employeeverification

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"
)

// Artifact is Host metadata for one file produced by a Run. The binding
// fields come from employee_task_artifact, or from a provider-confirmed
// sandbox delivery of this Run's queue execution; never from the model.
type Artifact struct {
	// Ref overrides the evidence reference ("artifact:<id>" when empty).
	Ref          string
	AttachmentID string
	TaskID       string
	RunID        string
	QueueTaskID  string
	GoalRevision int64
	Filename     string
	ContentType  string
	SHA256       string
	Size         int64
	State        string
}

// Delivery is one provider delivery attempt of the Run's result notice.
type Delivery struct {
	ActionID       string
	State          string
	ConversationID string
	MessageID      string
}

// runEvidence is the structured evidence a deterministic checker may read.
// The Run's assistant text and queue exit status are deliberately absent.
type runEvidence struct {
	RunID        string
	TaskID       string
	QueueTaskID  string
	GoalRevision int64
	Artifacts    []Artifact
	// Bytes holds the stored plaintext per attachment ID, read outside locks.
	Bytes      map[string][]byte
	Deliveries []Delivery
	// Delivered are files the provider confirmed this Run's execution sent
	// into the origin conversation; their bytes were downloaded by the Host
	// outside locks. DeliveredDigest binds the receipt set they came from.
	Delivered       []Artifact
	DeliveredDigest string
	// Unreadable lists delivered messages whose files the Host could not
	// read (identity changed, unverifiable record, size bound).
	Unreadable []string
}

// absenceRef names an "absent" verdict. A verdict that also saw delivered
// receipts gets its own reference, so it never collides with a Host-only
// verdict written by a replica that could not read deliveries.
func (e runEvidence) absenceRef() string {
	if e.DeliveredDigest != "" {
		return "run-artifacts:" + e.RunID + "/delivered"
	}
	return "run-artifacts:" + e.RunID
}

func isAbsenceRef(ref string) bool { return strings.HasPrefix(ref, "run-artifacts:") }

func (a Artifact) evidenceRef() string {
	if a.Ref != "" {
		return a.Ref
	}
	return "artifact:" + a.AttachmentID
}

// observation is one checker result before it becomes a durable Record.
type observation struct {
	Check          Check
	Outcome        Outcome
	EvidenceRef    string
	EvidenceSHA256 string
	Detail         string
}

// manifestDigest binds an "absent" verdict to the exact artifact list read.
func (e runEvidence) manifestDigest() string {
	lines := make([]string, 0, len(e.Artifacts))
	for _, a := range e.Artifacts {
		lines = append(lines, a.AttachmentID+"|"+a.SHA256+"|"+a.State+"|"+a.Filename)
	}
	sort.Strings(lines)
	body := e.RunID + "\n" + strings.Join(lines, "\n")
	if e.DeliveredDigest != "" {
		// Kept out of the v1 form when there is no delivery, so replicas of
		// either version compute the same digest for Host-only evidence.
		body += "\ndelivered:" + e.DeliveredDigest
	}
	return sha256Hex([]byte(body))
}

// hostManifestDigest covers only Host-held artifacts; the delivered receipt
// set is re-validated separately because its bytes come from the provider.
func (e runEvidence) hostManifestDigest() string {
	host := e
	host.DeliveredDigest = ""
	return host.manifestDigest()
}

func (e runEvidence) artifactNames() string {
	names := make([]string, 0, len(e.Artifacts)+len(e.Delivered))
	for _, a := range append(append([]Artifact(nil), e.Artifacts...), e.Delivered...) {
		names = append(names, a.Filename)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	return clip(strings.Join(names, ", "), 400)
}

// boundArtifacts returns only artifacts of this exact Task, Run, queue and
// goal revision whose name matches. A same-named file from another Run is
// never evidence (the GawkBot "check in the task's real workdir" rule).
func (e runEvidence) boundArtifacts(name string) []Artifact {
	var out []Artifact
	for _, a := range append(append([]Artifact(nil), e.Artifacts...), e.Delivered...) {
		if a.Filename == name && a.RunID == e.RunID && a.TaskID == e.TaskID && a.QueueTaskID == e.QueueTaskID && a.GoalRevision == e.GoalRevision {
			out = append(out, a)
		}
	}
	return out
}

// evaluate is pure and deterministic: identical evidence yields identical
// observations. Unknown kinds fail closed.
func evaluate(c Check, e runEvidence) []observation {
	switch c.Kind {
	case KindArtifactContents:
		return evaluateArtifacts(c, e, artifactContents)
	case KindExecutionOutput:
		if c.Command != "" {
			return []observation{{Check: c, Outcome: OutcomeUnknown, EvidenceRef: "host-command:unavailable", EvidenceSHA256: sha256Hex([]byte(c.Command)),
				Detail: "no Host-run command executor exists; a sandbox-reported exit status or an assistant claim is not proof"}}
		}
		return evaluateArtifacts(c, e, expectedOutput)
	case KindDeliveryReceipt:
		return evaluateDeliveries(c, e)
	default:
		raw, _ := json.Marshal(c)
		return []observation{{Check: c, Outcome: OutcomeFailed, EvidenceRef: "check-kind:" + clip(string(c.Kind), 64), EvidenceSHA256: sha256Hex(raw),
			Detail: fmt.Sprintf("unknown verification kind %q fails closed", clip(string(c.Kind), 64))}}
	}
}

func evaluateArtifacts(c Check, e runEvidence, judge func(Check, Artifact, []byte) (Outcome, string)) []observation {
	matches := e.boundArtifacts(c.File)
	if len(matches) == 0 && len(e.Unreadable) > 0 {
		// A delivered file exists that the Host could not read: its name is
		// unknown, so neither pass nor fail is justified.
		return []observation{{Check: c, Outcome: OutcomeUnknown, EvidenceRef: e.absenceRef(), EvidenceSHA256: e.manifestDigest(),
			Detail: fmt.Sprintf("no readable artifact named %s; %d delivered file(s) could not be read by the Host (%s)", c.File, len(e.Unreadable), clip(strings.Join(e.Unreadable, "; "), 300))}}
	}
	if len(matches) == 0 {
		return []observation{{Check: c, Outcome: OutcomeFailed, EvidenceRef: e.absenceRef(), EvidenceSHA256: e.manifestDigest(),
			Detail: fmt.Sprintf("this run produced no artifact named %s (run artifacts: %s)", c.File, e.artifactNames())}}
	}
	out := make([]observation, 0, len(matches))
	for _, a := range matches {
		o := observation{Check: c, EvidenceRef: a.evidenceRef(), EvidenceSHA256: a.SHA256}
		data, ok := e.Bytes[a.AttachmentID]
		switch {
		case a.State != "ready":
			o.Outcome, o.Detail = OutcomeUnknown, fmt.Sprintf("artifact %s is not settled (state %s)", a.Filename, a.State)
		case !sha256Pattern.MatchString(a.SHA256):
			o.Outcome, o.Detail, o.EvidenceSHA256 = OutcomeFailed, "artifact has no valid recorded sha256", sha256Hex([]byte(a.AttachmentID))
		case !ok && a.Size > MaxVerifiedArtifactBytes:
			o.Outcome, o.Detail = OutcomeUnknown, fmt.Sprintf("artifact %s is %d bytes, above the %d-byte verification bound", a.Filename, a.Size, MaxVerifiedArtifactBytes)
		case !ok:
			o.Outcome, o.Detail = OutcomeUnknown, fmt.Sprintf("artifact %s bytes were not read", a.Filename)
		case sha256Hex(data) != a.SHA256:
			o.Outcome, o.Detail = OutcomeFailed, fmt.Sprintf("stored bytes of %s do not match the recorded sha256", a.Filename)
		default:
			o.Outcome, o.Detail = judge(c, a, data)
		}
		out = append(out, o)
	}
	return out
}

func artifactText(data []byte) string {
	return string(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
}

func artifactContents(c Check, a Artifact, data []byte) (Outcome, string) {
	if len(data) == 0 {
		return OutcomeFailed, fmt.Sprintf("%s is empty", a.Filename)
	}
	facts := []string{fmt.Sprintf("%s sha256:%s", a.Filename, a.SHA256[:12])}
	if c.SHA256 != "" {
		if a.SHA256 != c.SHA256 {
			return OutcomeFailed, fmt.Sprintf("%s sha256 is %s, expected %s", a.Filename, a.SHA256[:12], c.SHA256[:12])
		}
		facts = append(facts, "exact bytes")
	}
	text := artifactText(data)
	if len(c.Contains) > 0 {
		if !utf8.ValidString(text) {
			return OutcomeFailed, fmt.Sprintf("%s is not UTF-8 text", a.Filename)
		}
		var missing []string
		for _, literal := range c.Contains {
			if !strings.Contains(text, literal) {
				missing = append(missing, literal)
			}
		}
		if len(missing) > 0 {
			return OutcomeFailed, fmt.Sprintf("%s does not contain %s", a.Filename, quoteList(missing))
		}
		facts = append(facts, "contains "+quoteList(c.Contains))
	}
	if c.DataRows != nil || len(c.Columns) > 0 {
		header, rows, err := parseTable(a.Filename, text)
		if err != nil {
			return OutcomeFailed, fmt.Sprintf("%s is not a valid table: %v", a.Filename, err)
		}
		if len(c.Columns) > 0 {
			present := map[string]bool{}
			for _, h := range header {
				present[strings.TrimSpace(h)] = true
			}
			var missing []string
			for _, column := range c.Columns {
				if !present[column] {
					missing = append(missing, column)
				}
			}
			if len(missing) > 0 {
				return OutcomeFailed, fmt.Sprintf("%s header lacks columns %s (header: %s)", a.Filename, quoteList(missing), clip(strings.Join(header, ","), 300))
			}
			facts = append(facts, "columns "+quoteList(c.Columns))
		}
		if c.DataRows != nil {
			if rows != *c.DataRows {
				return OutcomeFailed, fmt.Sprintf("%s has %d data rows, expected %d", a.Filename, rows, *c.DataRows)
			}
			facts = append(facts, fmt.Sprintf("%d data rows", rows))
		}
	}
	return OutcomePassed, strings.Join(facts, "; ")
}

// parseTable reads a CSV/TSV artifact. The first record is the header; data
// rows exclude it. Blank lines are ignored by encoding/csv.
func parseTable(name, text string) ([]string, int, error) {
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	if strings.HasSuffix(strings.ToLower(name), ".tsv") {
		reader.Comma = '\t'
	}
	records, err := reader.ReadAll()
	if err != nil {
		return nil, 0, err
	}
	if len(records) == 0 {
		return nil, 0, fmt.Errorf("no header row")
	}
	return records[0], len(records) - 1, nil
}

func expectedOutput(c Check, a Artifact, data []byte) (Outcome, string) {
	text := strings.TrimSpace(artifactText(data))
	got := text
	if c.Field != "" {
		var object map[string]json.RawMessage
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if err := decoder.Decode(&object); err != nil {
			return OutcomeFailed, fmt.Sprintf("%s is not a JSON object", a.Filename)
		}
		value, ok := object[c.Field]
		if !ok {
			return OutcomeFailed, fmt.Sprintf("%s has no field %s", a.Filename, c.Field)
		}
		var s string
		if json.Unmarshal(value, &s) == nil {
			got = s
		} else {
			got = strings.TrimSpace(string(value))
		}
	}
	if sameValue(got, c.Expect) {
		return OutcomePassed, fmt.Sprintf("%s sha256:%s %s= %s", a.Filename, a.SHA256[:12], fieldLabel(c.Field), clip(c.Expect, 80))
	}
	return OutcomeFailed, fmt.Sprintf("%s %sis %s, expected %s", a.Filename, fieldLabel(c.Field), clip(got, 80), clip(c.Expect, 80))
}

func fieldLabel(field string) string {
	if field == "" {
		return ""
	}
	return "field " + field + " "
}

// sameValue compares numbers numerically (5050 == 5050.0) and everything
// else as exact trimmed strings.
func sameValue(got, want string) bool {
	got, want = strings.TrimSpace(got), strings.TrimSpace(want)
	a, okA := new(big.Rat).SetString(got)
	b, okB := new(big.Rat).SetString(want)
	if okA && okB {
		return a.Cmp(b) == 0
	}
	return got == want
}

func evaluateDeliveries(c Check, e runEvidence) []observation {
	if len(e.Deliveries) == 0 {
		return []observation{{Check: c, Outcome: OutcomeUnknown, EvidenceRef: "run-delivery:" + e.RunID + ":none", EvidenceSHA256: sha256Hex([]byte("run-delivery:" + e.RunID)),
			Detail: "no delivery of this run's result is recorded"}}
	}
	out := make([]observation, 0, len(e.Deliveries))
	for _, d := range e.Deliveries {
		o := observation{Check: c, EvidenceRef: "response-action:" + clip(d.ActionID, 400) + "@" + d.State,
			EvidenceSHA256: sha256Hex([]byte(d.ActionID + "|" + d.State + "|" + d.ConversationID + "|" + d.MessageID))}
		switch {
		case d.State == "delivered" && d.MessageID != "":
			o.Outcome, o.Detail = OutcomePassed, "provider delivered message "+clip(d.MessageID, 120)+"; delivery proves receipt only, not correctness"
		case d.State == "failed" || d.State == "cancelled":
			o.Outcome, o.Detail = OutcomeFailed, "delivery "+d.State
		default:
			o.Outcome, o.Detail = OutcomeUnknown, "delivery state "+clip(d.State, 32)+" is not a provider receipt"
		}
		out = append(out, o)
	}
	return out
}

func quoteList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, fmt.Sprintf("%q", clip(v, 60)))
	}
	return clip(strings.Join(quoted, ", "), 300)
}

// countedRecords drops "absent" verdicts once any record over real evidence
// exists for the check: absence is the weakest evidence, and a replica that
// could not see an evidence source must not override one that read the bytes.
func countedRecords(records []Record) []Record {
	real := false
	for _, r := range records {
		real = real || !isAbsenceRef(r.EvidenceRef)
	}
	if !real {
		return records
	}
	out := make([]Record, 0, len(records))
	for _, r := range records {
		if !isAbsenceRef(r.EvidenceRef) {
			out = append(out, r)
		}
	}
	return out
}

// checkStatus folds the records of one check for one Run. For content checks
// a failure over real evidence dominates (any wrong copy fails); for delivery
// any confirmed receipt suffices.
func checkStatus(kind CheckKind, records []Record) Outcome {
	passed, failed := false, false
	for _, r := range countedRecords(records) {
		passed = passed || r.Outcome == OutcomePassed
		failed = failed || r.Outcome == OutcomeFailed
	}
	if kind == KindDeliveryReceipt {
		if passed {
			return OutcomePassed
		}
		if failed {
			return OutcomeFailed
		}
		return OutcomeUnknown
	}
	if failed {
		return OutcomeFailed
	}
	if passed {
		return OutcomePassed
	}
	return OutcomeUnknown
}

// gateFor computes the Run's gate against the spec from retained records.
func gateFor(spec Spec, records []Record) Gate {
	if spec.State != SpecActive || len(spec.Checks) == 0 {
		return Gate{Status: GateNone}
	}
	g := Gate{Status: GatePassed, SpecRevision: spec.Revision, SpecDigest: spec.Digest}
	byCheck := map[string][]Record{}
	for _, r := range records {
		byCheck[r.CheckID] = append(byCheck[r.CheckID], r)
	}
	anyFailed, anyPending := false, false
	for _, c := range spec.Checks {
		if c.Optional {
			continue
		}
		status := checkStatus(c.Kind, byCheck[c.ID])
		if !knownKind(c.Kind) && status != OutcomeFailed {
			status = OutcomeUnknown
		}
		switch status {
		case OutcomePassed:
			if correctnessKind(c.Kind) {
				g.Correct = true
			}
		case OutcomeFailed:
			anyFailed = true
			for _, r := range countedRecords(byCheck[c.ID]) {
				if r.Outcome == OutcomeFailed {
					g.Failed = append(g.Failed, r)
				}
			}
		default:
			anyPending = true
			g.Pending = append(g.Pending, c.ID)
		}
	}
	switch {
	case anyFailed:
		g.Status = GateFailed
		g.Correct = false
	case anyPending:
		g.Status = GatePending
		g.Correct = false
	}
	return g
}
