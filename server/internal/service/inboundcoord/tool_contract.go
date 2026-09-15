package inboundcoord

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
)

// toolContract is what Host already knows to be valid for this round. Putting
// it into the schema (enums, consts, absent kinds) lets the model call a tool
// correctly the first time instead of discovering the rules through failure
// hints: source refs of the current window, read refs that exist, Issue ids
// that were actually recalled, the current memory revision and the boundary
// sentences a decline may quote.
type toolContract struct {
	sourceRefs           []string
	requiredResponseRefs []string
	stateRefs            []string
	issueIDs             []string
	memoryRevision       int64
	boundaryQuotes       []string
}

// boundaryQuoteLimit bounds the decline quote options offered in the schema.
const boundaryQuoteLimit = 24

func toolContractFor(turn Turn) toolContract {
	contract := toolContract{memoryRevision: turn.SceneMemoryRevision, requiredResponseRefs: requiredResponseRefs(turn)}
	for i := range windowUtterances(turn) {
		contract.sourceRefs = append(contract.sourceRefs, fmt.Sprintf("u%d", i+1))
	}
	for _, read := range turn.CoordinationReads {
		// report_status may cite work evidence only: assoc_recall, work_state
		// and coordination_state snapshots. A history snapshot is dialogue,
		// not execution state, and Host rejects it as a state_ref.
		isState := read.Tool == toolAssocRecall || read.Tool == toolWorkState || (read.Tool == toolContextRead && read.Kind == coordinationStateKind)
		if isState && strings.TrimSpace(read.ReadRef) != "" {
			contract.stateRefs = append(contract.stateRefs, read.ReadRef)
		}
	}
	contract.issueIDs = append([]string(nil), turn.recalledIssueIDs...)
	sort.Strings(contract.issueIDs)
	contract.boundaryQuotes = boundaryQuoteOptions(turn)
	return contract
}

// boundaryQuoteOptions lists the sentences a decline may quote verbatim. They
// come only from sources the routing model can see: a loaded short contract,
// the bounded persona and reply_tone, and the current window. The Host-held
// full job policy is deliberately excluded. Each option is a substring of its
// source, so suppliedConstraintQuote accepts it unchanged.
func boundaryQuoteOptions(turn Turn) []string {
	seen := map[string]bool{}
	var out []string
	add := func(sentence string) bool {
		sentence = strings.TrimSpace(sentence)
		if n := utf8.RuneCountInString(sentence); n < 2 || n > 300 || seen[sentence] {
			return len(out) < boundaryQuoteLimit
		}
		if workRequestUtterance(sentence) || quoteIsCurrentWorkUtterance(sentence, turn) {
			return len(out) < boundaryQuoteLimit
		}
		seen[sentence] = true
		out = append(out, sentence)
		return len(out) < boundaryQuoteLimit
	}
	// A loaded short contract is quoted entry by entry; each entry is a
	// substring of its canonical JSON, which is what Host validates against.
	if contract, state := currentCoordinatorContract(turn); state == coordinatorcontract.StateLoaded && contract != nil {
		for _, group := range [][]string{contract.Constraints, contract.MustDelegate, contract.ClarifyWhen} {
			for _, entry := range group {
				if !add(entry) {
					return out
				}
			}
		}
	}
	sources := []string{configuredPersona(turn), configuredReplyTone(turn)}
	for _, utterance := range windowUtterances(turn) {
		sources = append(sources, utterance.Text)
	}
	for _, source := range sources {
		// The whole bounded field is quotable too, so a short restriction such
		// as a three-character reply_tone is never lost to sentence splitting.
		if !add(source) {
			return out
		}
		for _, sentence := range splitBoundarySentences(source) {
			if !add(sentence) {
				return out
			}
		}
	}
	return out
}

func splitBoundarySentences(text string) []string {
	var out []string
	var current strings.Builder
	flush := func() {
		sentence := strings.TrimSpace(current.String())
		current.Reset()
		if n := utf8.RuneCountInString(sentence); n >= 2 && n <= 300 {
			out = append(out, sentence)
		}
	}
	for _, r := range text {
		switch r {
		case '。', '！', '？', '；', '\n', '!', '?', ';':
			flush()
		case '.':
			// A period ends an English sentence; inside identifiers or numbers
			// it does not, but a too-short fragment is dropped by the length gate.
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return out
}

func stringEnum(values []string) map[string]any {
	return map[string]any{"type": "string", "enum": append([]string(nil), values...)}
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func removeKind(kinds []string, kind string) []string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		if k != kind {
			out = append(out, k)
		}
	}
	return out
}

// coordinatorWorkStateToolFor exposes only recalled Issue ids: the schema is
// the contract, so an id that was never recalled cannot be requested.
func coordinatorWorkStateToolFor(issueIDs []string) openai.ChatCompletionToolUnionParam {
	schema := recalledIssueIDSchema("Issue UUID copied exactly from assoc_recall.")
	if len(issueIDs) > 0 {
		schema["enum"] = append([]string(nil), issueIDs...)
	}
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        toolWorkState,
		Description: openai.String("Read this turn's bounded status and original-goal snapshot of a recalled Issue. Successful same-argument reads reuse the Host read_ref; repeating cannot expand truncated/complete=false summaries. Unavailable reads may be retried. No comments, execution results or chat bodies. Cite read_ref in report_status; unknown execution/delivery stays unknown."),
		Parameters:  shared.FunctionParameters{"type": "object", "additionalProperties": false, "required": []string{"issue_id"}, "properties": map[string]any{"issue_id": schema}},
	})
}
