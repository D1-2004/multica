package inboundcoord

import (
	"fmt"
	"strings"
)

type finishQuoteOption struct {
	Ref  string `json:"ref"`
	Text string `json:"text"`
}

type finishQuoteOptions struct {
	Requests   []finishQuoteOption `json:"requests"`
	Candidates []finishQuoteOption `json:"candidates"`
}

// Quotes are evidence selections, not a transcription task. The model selects
// a local ID; Host retains the exact text, including literal escapes.
func finishQuotes(turn Turn, decision Decision) finishQuoteOptions {
	var options finishQuoteOptions
	add := func(dst *[]finishQuoteOption, prefix, raw string) {
		text := strings.TrimSpace(raw)
		if text == "" {
			return
		}
		text = clipRunes(text, 80)
		for _, old := range *dst {
			if old.Text == text {
				return
			}
		}
		*dst = append(*dst, finishQuoteOption{Ref: fmt.Sprintf("%s%d", prefix, len(*dst)+1), Text: text})
	}
	for _, u := range windowUtterances(turn) {
		if len(options.Requests) < WindowPlanMaxItems {
			add(&options.Requests, "q", u.Text)
		}
	}
	if turn.Loop == LoopTaskFinished {
		add(&options.Requests, "q", turn.TaskResult)
	}
	if len(options.Requests) == 0 {
		add(&options.Requests, "q", "[empty_window]")
	}
	workIndex := 0
	for _, a := range decision.CoordinationActions {
		if a.Kind == "start_work" || a.Kind == "continue_work" {
			if workIndex < len(decision.Items) {
				add(&options.Candidates, "c", decision.Items[workIndex].Purpose)
			}
			workIndex++
		}
		add(&options.Candidates, "c", a.Reply)
	}
	if len(options.Candidates) == 0 && decision.Action == ActionSilence {
		add(&options.Candidates, "c", "[silence]")
	}
	return options
}

func finishQuoteRefs(options []finishQuoteOption) []string {
	refs := make([]string, 0, len(options))
	for _, q := range options {
		refs = append(refs, q.Ref)
	}
	return refs
}

func bindFinishQuotes(result *finishCheckResult, options finishQuoteOptions) error {
	lookup := func(ref string, choices []finishQuoteOption) (string, bool) {
		for _, choice := range choices {
			if choice.Ref == ref {
				return choice.Text, true
			}
		}
		return "", false
	}
	request, ok := lookup(result.RequestQuoteRef, options.Requests)
	if !ok {
		return fmt.Errorf("finish check selected unknown request_quote_ref")
	}
	candidate, ok := lookup(result.CandidateQuoteRef, options.Candidates)
	if !ok {
		return fmt.Errorf("finish check selected unknown candidate_quote_ref")
	}
	result.RequestQuote, result.CandidateQuote = request, candidate
	return nil
}
