package digest

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// Rejection is one refused operation, recorded on the run (bounded) and fed
// back to the model in the single repair round.
type Rejection struct {
	Index  int    `json:"i"`
	Op     string `json:"op,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Reason string `json:"reason"`
}

// Reasons the model can fix by resubmitting; everything else is a policy
// refusal that a repair round would only repeat.
var fixableReasons = map[string]bool{
	"invalid_op": true, "invalid_kind": true, "invalid_subject": true, "unknown_evidence": true,
	"duplicate_evidence": true, "quote_not_verbatim": true, "invalid_quote": true,
	"subject_literal_not_in_evidence": true, "subject_not_grounded": true, "unknown_item": true,
	"no_tool_call": true, "invalid_arguments": true, "output_truncated": true,
}

type plannedUpsert struct {
	Index int
	Tag   string
	Key   string
	Op    FactUpsert
}

type plannedRetract struct {
	Index int
	Tag   string
	Fact  Fact
}

type plan struct {
	Upserts    []plannedUpsert
	Retracts   []plannedRetract
	Rejections []Rejection
	Proposed   int
	used       map[string]bool
	retracted  map[string]bool
}

func newPlan() *plan { return &plan{used: map[string]bool{}, retracted: map[string]bool{}} }

func (p *plan) fixable() bool {
	for _, r := range p.Rejections {
		if fixableReasons[r.Reason] {
			return true
		}
	}
	return false
}

func (p *plan) onlyPolicyRejections() bool {
	for _, r := range p.Rejections {
		if fixableReasons[r.Reason] {
			return false
		}
	}
	return true
}

var (
	phonePattern   = regexp.MustCompile(`(^|[^0-9])1[3-9][0-9]{9}([^0-9]|$)`)
	emailPattern   = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	idCardPattern  = regexp.MustCompile(`(^|[^0-9])[1-9][0-9]{16}[0-9Xx]([^0-9]|$)`)
	secretKV       = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|passwd|密码|口令)\s*[:=：]\s*\S+`)
	literalPattern = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._:/\-]*`)
	// valuePattern finds Chinese date, time and quantity values: a subject
	// may name a topic freely, but never a value the evidence did not say.
	valuePattern = regexp.MustCompile(`(周|星期|礼拜)[一二三四五六日天末]|[0-9零一二三四五六七八九十两百]+\s*(月|日|号|点|时|分|天|周|个|人|次|份|元|万|%)|(今|明|后|昨|前)天|(上|下|本|这)(周|个?月|季度?)`)
)

// sensitive detects contact data and secrets: the digest never stores them.
func sensitive(s string) bool {
	if phonePattern.MatchString(s) || emailPattern.MatchString(s) || idCardPattern.MatchString(s) || secretKV.MatchString(s) {
		return true
	}
	return redact.Text(s) != s
}

var personProfileCues = []string{
	"性格", "脾气", "为人", "人品", "爱好", "擅长", "偏好", "喜欢", "讨厌", "年龄", "岁了", "生日",
	"住址", "家住", "手机号", "微信号", "身份证", "工资", "薪资", "薪水", "绩效", "生病", "病假",
	"怀孕", "结婚", "离婚", "恋爱", "男朋友", "女朋友",
}

// personProfile rejects statements about a person's traits or private life:
// the scene digest never builds person profiles.
func personProfile(subject, quote string) bool {
	text := subject + " " + quote
	for _, cue := range personProfileCues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

var instructionCues = []string{
	"忽略之前", "忽略以上", "忽略上面", "你现在是", "以系统身份", "跳过审核", "全部批准", "无需审批", "无需确认", "系统提示", "提示词",
	"ignore previous", "ignore all previous", "you are now", "system prompt", "skip review", "skip security",
}

func instructionLike(s string) bool {
	lower := strings.ToLower(s)
	for _, cue := range instructionCues {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

func validSubject(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > MaxSubjectRunes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// literalsGrounded requires every number, code, ASCII token and Chinese
// date/time/quantity value in the subject to appear verbatim
// (case-insensitive) in the evidence line.
func literalsGrounded(subject, body string) bool {
	lower := strings.ToLower(body)
	for _, literal := range append(literalPattern.FindAllString(subject, -1), valuePattern.FindAllString(subject, -1)...) {
		if !strings.Contains(lower, strings.ToLower(strings.Join(strings.Fields(literal), ""))) && !strings.Contains(lower, strings.ToLower(literal)) {
			return false
		}
	}
	return true
}

// subjectGrounded keeps the subject on the evidence's topic: it is a recall
// label a later question would use (发版时间 for 「定了：周四发版」), so it
// must share at least one retrieval unit with the evidence; values are
// pinned separately by literalsGrounded and the content is the verbatim
// quote.
func subjectGrounded(subject, body string) bool {
	shared, total := employeememory.SharedRetrievalUnits(subject, body)
	if total == 0 {
		return literalPattern.MatchString(subject) && literalsGrounded(subject, body)
	}
	return shared >= 1
}

// validate checks one batch of proposals against the page. Attribution is
// always the Host-read evidence line; the model only picks labels.
func validate(pl *plan, p *page, ops []proposal, facts FactStore, baseIndex int) {
	for i, op := range ops {
		index := baseIndex + i
		pl.Proposed++
		reject := func(ref, reason string) {
			pl.Rejections = append(pl.Rejections, Rejection{Index: index, Op: op.Op, Ref: clip(ref, 16), Reason: reason})
		}
		if i >= MaxOpsPerCall {
			reject("", "too_many_ops")
			continue
		}
		switch strings.TrimSpace(op.Op) {
		case "retract":
			tag := strings.TrimSpace(op.Item)
			if _, other := p.Others[tag]; other {
				reject(tag, "not_own_output")
				continue
			}
			fact, own := p.Own[tag]
			if !own {
				reject(tag, "unknown_item")
				continue
			}
			if pl.retracted[tag] {
				continue
			}
			pl.retracted[tag] = true
			pl.Retracts = append(pl.Retracts, plannedRetract{Index: index, Tag: tag, Fact: fact})
		case "upsert":
			tag := strings.TrimSpace(op.Evidence)
			kind := strings.TrimSpace(op.Kind)
			subject := strings.TrimSpace(op.Subject)
			quote := op.Quote
			if kind != KindFact && kind != KindDecision && kind != KindOpenItem {
				reject(tag, "invalid_kind")
				continue
			}
			if !validSubject(subject) {
				reject(tag, "invalid_subject")
				continue
			}
			line, ok := p.Evidence[tag]
			if !ok {
				reject(tag, "unknown_evidence")
				continue
			}
			if pl.used[tag] {
				reject(tag, "duplicate_evidence")
				continue
			}
			if n := utf8.RuneCountInString(quote); n < 4 || n > MaxQuoteRunes || !utf8.ValidString(quote) {
				reject(tag, "invalid_quote")
				continue
			}
			body := line.Body
			if !strings.Contains(body, quote) {
				reject(tag, "quote_not_verbatim")
				continue
			}
			if !literalsGrounded(subject, body) {
				reject(tag, "subject_literal_not_in_evidence")
				continue
			}
			if !subjectGrounded(subject, body) {
				reject(tag, "subject_not_grounded")
				continue
			}
			if sensitive(quote) || sensitive(subject) {
				reject(tag, "sensitive")
				continue
			}
			if personProfile(subject, quote) {
				reject(tag, "person_profile")
				continue
			}
			if instructionLike(subject) || instructionLike(quote) {
				reject(tag, "instruction_like")
				continue
			}
			key, err := facts.HostKey(kind, subject)
			if err != nil {
				reject(tag, "invalid_subject")
				continue
			}
			if humanRecordExists(p, kind, key) {
				reject(tag, "human_record_exists")
				continue
			}
			pl.used[tag] = true
			pl.Upserts = append(pl.Upserts, plannedUpsert{Index: index, Tag: tag, Key: key, Op: FactUpsert{Kind: kind, Subject: subject, Quote: quote, Evidence: line}})
		default:
			reject("", "invalid_op")
		}
	}
}

// humanRecordExists: a member's own record of the same subject always wins;
// the writer does not create a competing candidate.
func humanRecordExists(p *page, kind, key string) bool {
	for _, f := range p.Others {
		if f.Type == kind && f.Key == key {
			return true
		}
	}
	return false
}

// boundRejections keeps the stored list within the 4 KiB column bound.
func boundRejections(in []Rejection) []Rejection {
	out := make([]Rejection, 0, len(in))
	size := 2
	for _, r := range in {
		size += len(r.Op) + len(r.Ref) + len(r.Reason) + 40
		if size > 3800 {
			break
		}
		out = append(out, r)
	}
	return out
}
