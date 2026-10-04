package employeetask

import (
	"fmt"
	"strings"
)

type CompletionNoticeMode string

const (
	CompletionNoticeAlways         CompletionNoticeMode = "always"
	CompletionNoticeIfNotDelivered CompletionNoticeMode = "if_not_delivered"
)

// CompletionNoticePolicy preserves an explicit requester delivery constraint.
// The Host verifies its quote against the selected frozen message. It never
// makes an execution result or a client-reported send into delivery evidence.
type CompletionNoticePolicy struct {
	Mode             CompletionNoticeMode `json:"mode"`
	RequireDelivery  string               `json:"require_delivery,omitempty"`
	SourceRef        string               `json:"source_ref,omitempty"`
	InstructionQuote string               `json:"instruction_quote,omitempty"`
}

func NormalizeCompletionNoticePolicy(p CompletionNoticePolicy, sourceRef string) (CompletionNoticePolicy, error) {
	if p.Mode == "" {
		p.Mode = CompletionNoticeAlways
	}
	switch p.Mode {
	case CompletionNoticeAlways:
		if p.RequireDelivery != "" || p.SourceRef != "" || p.InstructionQuote != "" {
			return p, fmt.Errorf("%w: always notice must not carry a delivery exemption", ErrInvalid)
		}
	case CompletionNoticeIfNotDelivered:
		if p.RequireDelivery != "file" || p.SourceRef != sourceRef || strings.TrimSpace(p.InstructionQuote) == "" || len(p.InstructionQuote) > 2000 {
			return p, fmt.Errorf("%w: file delivery exemption requires selected source evidence", ErrInvalid)
		}
	default:
		return p, fmt.Errorf("%w: unsupported completion notice policy", ErrInvalid)
	}
	return p, nil
}
