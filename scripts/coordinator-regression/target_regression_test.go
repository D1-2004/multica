package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/llm"
	openai "github.com/openai/openai-go/v3"
)

const targetRegressionInputLimit = 32 << 20

type targetRegressionInput struct {
	Cases []targetRegressionCase `json:"cases"`
}

type targetRegressionCase struct {
	ID           string                     `json:"id"`
	Turn         Turn                       `json:"turn"`
	Reads        []targetRegressionReadRule `json:"reads"`
	History      []HistoryLine              `json:"history"`
	HistoryError string                     `json:"history_error"`
}

type targetRegressionReadRule struct {
	Tool     string            `json:"tool"`
	Args     map[string]string `json:"args"`
	Result   json.RawMessage   `json:"result"`
	Error    string            `json:"error"`
	patterns map[string]*regexp.Regexp
}

type targetRegressionReadCall struct {
	Name      string          `json:"name"`
	Args      string          `json:"args"`
	Result    json.RawMessage `json:"result"`
	Error     string          `json:"error"`
	RuleIndex int             `json:"rule_index"`
}

type targetRegressionHistoryCall struct {
	ConversationID string        `json:"conversation_id"`
	HistoryBefore  time.Time     `json:"history_before"`
	Result         []HistoryLine `json:"result"`
	Error          string        `json:"error"`
}

type targetRegressionQuoteCheck struct {
	ModelCall      int    `json:"model_call"`
	SourceRef      string `json:"source_ref"`
	QuoteHash      string `json:"quote_sha256"`
	QuoteRunes     int    `json:"quote_runes"`
	FullPresent    bool   `json:"full_present"`
	ExcerptRunes   int    `json:"excerpt_runes"`
	ExcerptPresent bool   `json:"excerpt_present"`
}

type targetRegressionResult struct {
	ID                 string                        `json:"id"`
	CaseIndex          int                           `json:"case_index"`
	Repeat             int                           `json:"repeat"`
	FixtureHash        string                        `json:"fixture_sha256"`
	Decision           Decision                      `json:"decision"`
	RunError           string                        `json:"run_error"`
	ElapsedMS          int64                         `json:"elapsed_ms"`
	Rounds             []frozenReplayRound           `json:"rounds"`
	Reads              []targetRegressionReadCall    `json:"read_calls"`
	HistoryCalls       []targetRegressionHistoryCall `json:"history_calls"`
	Policy             PolicyManifest                `json:"policy"`
	Contract           map[string]any                `json:"contract"`
	PrimaryQuoteChecks []targetRegressionQuoteCheck  `json:"primary_quote_checks"`
	Artifacts          []string                      `json:"artifacts"`
	ArtifactErrors     []string                      `json:"artifact_errors"`
	SemanticAssessment string                        `json:"semantic_assessment"`
}

// The oracle is deliberately external. This harness records real-model routing
// against frozen reads; a successful Go test does not establish business quality.
// Only this opt-in test can call the configured LLM. There is no DB, dispatcher,
// DWS client, checkpoint callback, or business-write tool implementation.
func TestTargetRegression(t *testing.T) {
	if os.Getenv("MULTICA_RUN_COORDINATOR_REPLAY") != "1" {
		t.Skip("real model target regression requires explicit opt-in")
	}
	inputPath, outputPath := os.Getenv("MULTICA_TARGET_REGRESSION_INPUT"), os.Getenv("MULTICA_TARGET_REGRESSION_OUTPUT")
	if inputPath == "" || outputPath == "" {
		t.Fatal("MULTICA_TARGET_REGRESSION_INPUT and OUTPUT are required")
	}
	repeats := 3
	if value := os.Getenv("MULTICA_TARGET_REGRESSION_REPEATS"); value != "" {
		var err error
		repeats, err = strconv.Atoi(value)
		if err != nil || repeats < 1 || repeats > 10 {
			t.Fatal("MULTICA_TARGET_REGRESSION_REPEATS must be between 1 and 10")
		}
	}
	filter, err := regexp.Compile(os.Getenv("MULTICA_TARGET_REGRESSION_CASE"))
	if err != nil {
		t.Fatal("invalid MULTICA_TARGET_REGRESSION_CASE regex")
	}
	file, err := os.Open(inputPath)
	if err != nil {
		t.Fatal("cannot open private target regression input")
	}
	body, readErr := io.ReadAll(io.LimitReader(file, targetRegressionInputLimit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(body) > targetRegressionInputLimit {
		t.Fatal("cannot read target regression input within the 32 MiB limit")
	}
	var input targetRegressionInput
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		t.Fatalf("invalid target regression input (%T); inspect the private input", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatal("input must contain one JSON object")
	}
	if len(input.Cases) == 0 || len(input.Cases) > 128 {
		t.Fatal("input requires between 1 and 128 cases")
	}
	selected, seen := 0, map[string]bool{}
	for i := range input.Cases {
		fixture := &input.Cases[i]
		if fixture.ID == "" || seen[fixture.ID] {
			t.Fatal("case ids must be nonempty and unique")
		}
		seen[fixture.ID] = true
		if filter.MatchString(fixture.ID) {
			selected++
		}
		for j := range fixture.Reads {
			rule := &fixture.Reads[j]
			if !targetRegressionReadOnlyTool(rule.Tool) {
				t.Fatalf("case %d read rule %d must name a read-only fixture tool", i+1, j+1)
			}
			rule.patterns = map[string]*regexp.Regexp{}
			for name, expression := range rule.Args {
				pattern, err := regexp.Compile(expression)
				if err != nil {
					t.Fatalf("case %d read rule %d has an invalid argument regex", i+1, j+1)
				}
				rule.patterns[name] = pattern
			}
		}
	}
	if selected == 0 {
		t.Fatal("case filter matched no input cases")
	}
	cfg := llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: coordinatorModel, MaxRetries: -1}
	if !llm.New(cfg).Enabled() {
		t.Fatal("opt-in regression requires LLM configuration; no credentials printed")
	}
	outputPath, err = filepath.Abs(outputPath)
	if err != nil {
		t.Fatal("invalid output path")
	}
	artifactRoot := filepath.Join(filepath.Dir(outputPath), "artifacts")
	if err := os.MkdirAll(artifactRoot, 0700); err != nil {
		t.Fatal("cannot create private artifact directory")
	}
	if err := os.Chmod(artifactRoot, 0700); err != nil {
		t.Fatal("cannot protect artifact directory")
	}
	artifactDir, err := os.MkdirTemp(artifactRoot, "target-regression-")
	if err != nil {
		t.Fatal("cannot create per-run artifact directory")
	}
	report := struct {
		StartedAt          time.Time                `json:"started_at"`
		CompletedAt        time.Time                `json:"completed_at"`
		InputHash          string                   `json:"input_sha256"`
		PolicyVersion      string                   `json:"policy_version"`
		AssemblyVersion    string                   `json:"assembly_version"`
		Repeats            int                      `json:"repeats"`
		SelectedCases      int                      `json:"selected_cases"`
		ArtifactDirectory  string                   `json:"artifact_directory"`
		Safety             string                   `json:"safety"`
		SemanticAssessment string                   `json:"semantic_assessment"`
		Cases              []targetRegressionResult `json:"cases"`
	}{StartedAt: time.Now().UTC(), InputHash: policyHash(string(body)), PolicyVersion: coordinatorPolicy.Version,
		AssemblyVersion: coordinatorPolicy.AssemblyVersion, Repeats: repeats, SelectedCases: selected, ArtifactDirectory: artifactDir,
		Safety:             "Real LLM with fixture-only read tools and history; no database, task creation, messages, memory writes or deployment.",
		SemanticAssessment: "not_evaluated; apply the external oracle to decisions and exact request artifacts", Cases: []targetRegressionResult{}}
	var mu sync.Mutex
	// A parent Cleanup runs after all parallel children finish, unlike code
	// immediately following t.Run when its children have called t.Parallel.
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		report.CompletedAt = time.Now().UTC()
		sort.Slice(report.Cases, func(i, j int) bool {
			if report.Cases[i].CaseIndex != report.Cases[j].CaseIndex {
				return report.Cases[i].CaseIndex < report.Cases[j].CaseIndex
			}
			return report.Cases[i].Repeat < report.Cases[j].Repeat
		})
		if err := targetRegressionWriteJSON(outputPath, report); err != nil {
			t.Errorf("cannot write private target regression report (%T)", err)
		}
		t.Logf("ungraded target regression captured %d runs; private report: %s", len(report.Cases), outputPath)
	})
	for index, fixture := range input.Cases {
		if !filter.MatchString(fixture.ID) {
			continue
		}
		t.Run(fmt.Sprintf("case-%03d", index+1), func(t *testing.T) {
			t.Parallel()
			fixtureJSON, _ := json.Marshal(fixture)
			for iteration := 1; iteration <= repeats; iteration++ {
				// Each repetition gets fresh mutable Turn slices/maps, read logs,
				// model observer and review cache. Fixture rules remain read-only.
				var isolated targetRegressionCase
				if err := json.Unmarshal(fixtureJSON, &isolated); err != nil {
					t.Fatal("cannot isolate fixture repetition")
				}
				reads := &targetRegressionTools{rules: fixture.Reads, calls: []targetRegressionReadCall{}}
				history := &targetRegressionHistory{scene: isolated.Turn.ConversationID, lines: isolated.History, failure: isolated.HistoryError, calls: []targetRegressionHistoryCall{}}
				observer := &targetRegressionCompleter{inner: &frozenReplayCompleter{client: llm.New(cfg)}, turn: isolated.Turn,
					caseIndex: index + 1, repeat: iteration, directory: artifactDir, artifacts: []string{}, artifactErrors: []string{}, quoteChecks: []targetRegressionQuoteCheck{}}
				coordinator := &Coordinator{Chat: observer, Tools: reads, DWSHistory: history}
				started := time.Now()
				ctx, cancel := context.WithTimeout(context.Background(), decisionTimeout)
				decision, runErr := coordinator.runLoop(ctx, isolated.Turn)
				cancel()
				result := targetRegressionResult{ID: fixture.ID, CaseIndex: index + 1, Repeat: iteration, FixtureHash: policyHash(string(fixtureJSON)),
					Decision: decision, RunError: targetRegressionError(runErr), ElapsedMS: time.Since(started).Milliseconds(),
					Rounds: observer.inner.rounds, Reads: reads.calls, HistoryCalls: history.calls, Policy: policyManifest(isolated.Turn),
					Contract: coordinatorContractMetadata(isolated.Turn), PrimaryQuoteChecks: observer.quoteChecks,
					Artifacts: observer.artifacts, ArtifactErrors: observer.artifactErrors, SemanticAssessment: "not_evaluated"}
				mu.Lock()
				report.Cases = append(report.Cases, result)
				mu.Unlock()
				if runErr != nil {
					t.Errorf("case %d repeat %d routing error (%T); inspect the private report", index+1, iteration, runErr)
				}
				if len(observer.artifactErrors) > 0 {
					t.Errorf("case %d repeat %d could not persist %d audit artifacts", index+1, iteration, len(observer.artifactErrors))
				}
			}
		})
	}
}

type targetRegressionTools struct {
	rules []targetRegressionReadRule
	calls []targetRegressionReadCall
}

func targetRegressionReadOnlyTool(name string) bool {
	return name == toolAssocRecall || name == toolWorkState || name == toolIssueGet || name == toolIssueCommentList
}

func (f *targetRegressionTools) Call(ctx context.Context, turn Turn, name, arguments string) (result string, err error) {
	call := targetRegressionReadCall{Name: name, Args: arguments, RuleIndex: -1}
	defer func() {
		if json.Valid([]byte(result)) {
			call.Result = json.RawMessage(result)
		}
		call.Error = targetRegressionError(err)
		f.calls = append(f.calls, call)
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !targetRegressionReadOnlyTool(name) {
		return "", fmt.Errorf("fixture has no business-write implementation for %q", name)
	}
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &args) != nil || args == nil {
		return "", errors.New("fixture read requires JSON object arguments")
	}
	if raw, present := args["conversation_id"]; present {
		var conversationID string
		if json.Unmarshal(raw, &conversationID) != nil {
			return "", errors.New("fixture scope error: conversation_id must be a string")
		}
		if conversationID != "" && conversationID != turn.ConversationID {
			return "", errors.New("fixture scope error: conversation_id is outside the current turn")
		}
	}
	for index, rule := range f.rules {
		if rule.Tool != name {
			continue
		}
		matches := true
		for field, expression := range rule.patterns {
			raw, present := args[field]
			value := string(raw)
			var text string
			if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &text) == nil {
				value = text
			}
			if !present || !expression.MatchString(value) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		call.RuleIndex = index
		if rule.Error != "" {
			return string(rule.Result), errors.New(rule.Error)
		}
		if len(rule.Result) == 0 {
			return "", errors.New("matched fixture read has no supplied result")
		}
		return string(rule.Result), nil
	}
	return "", fmt.Errorf("no frozen read rule matches %q", name)
}

type targetRegressionHistory struct {
	scene   string
	lines   []HistoryLine
	failure string
	calls   []targetRegressionHistoryCall
}

func (f *targetRegressionHistory) Load(ctx context.Context, turn Turn) (lines []HistoryLine, err error) {
	defer func() {
		f.calls = append(f.calls, targetRegressionHistoryCall{ConversationID: turn.ConversationID, HistoryBefore: turn.HistoryBefore, Result: append([]HistoryLine{}, lines...), Error: targetRegressionError(err)})
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if turn.ConversationID != f.scene {
		return nil, errors.New("fixture history is unavailable outside its conversation")
	}
	if f.failure != "" {
		return nil, errors.New(f.failure)
	}
	return append([]HistoryLine{}, f.lines...), nil
}

type targetRegressionCompleter struct {
	inner          *frozenReplayCompleter
	turn           Turn
	caseIndex      int
	repeat         int
	directory      string
	artifacts      []string
	artifactErrors []string
	quoteChecks    []targetRegressionQuoteCheck
	count          int
}

func (f *targetRegressionCompleter) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	f.count++
	callIndex := f.count
	tools := toolParamNames(params.Tools)
	if len(tools) != 1 || tools[0] != toolFinishCheck {
		f.quoteChecks = append(f.quoteChecks, targetRegressionCheckQuotes(params, f.turn, callIndex)...)
	}
	started := time.Now()
	response, callErr := f.inner.Chat(ctx, params)
	choices := []openai.ChatCompletionChoice{}
	if response != nil {
		choices = response.Choices
	}
	artifact := struct {
		CaseIndex int                            `json:"case_index"`
		Repeat    int                            `json:"repeat"`
		CallIndex int                            `json:"call_index"`
		Params    openai.ChatCompletionNewParams `json:"params"`
		Choices   []openai.ChatCompletionChoice  `json:"choices"`
		Error     string                         `json:"error"`
		ElapsedMS int64                          `json:"elapsed_ms"`
	}{f.caseIndex, f.repeat, callIndex, params, choices, targetRegressionError(callErr), time.Since(started).Milliseconds()}
	path := filepath.Join(f.directory, fmt.Sprintf("case-%03d-repeat-%02d-request-%02d.json", f.caseIndex, f.repeat, callIndex))
	if err := targetRegressionWriteJSON(path, artifact); err != nil {
		f.artifactErrors = append(f.artifactErrors, fmt.Sprintf("call %d: %v", callIndex, err))
	} else {
		f.artifacts = append(f.artifacts, path)
	}
	return response, callErr
}

func targetRegressionCheckQuotes(params openai.ChatCompletionNewParams, turn Turn, call int) []targetRegressionQuoteCheck {
	raw, _ := json.Marshal(params.Messages)
	var messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &messages) != nil {
		return nil
	}
	userText := ""
	for _, message := range messages {
		if message.Role == "user" {
			userText += message.Content + "\n"
		}
	}
	checks := []targetRegressionQuoteCheck{}
	for index, utterance := range windowUtterances(turn) {
		quote := utterance.ReplyToContent
		if quote == "" {
			continue
		}
		// buildUserPrompt uses Go %q inside text; decode Chat JSON first and
		// accept that exact escaped representation without guessing escapes.
		present := func(value string) bool {
			return strings.Contains(userText, value) || strings.Contains(userText, fmt.Sprintf("%q", value))
		}
		excerpt := clipRunes(quote, 800)
		checks = append(checks, targetRegressionQuoteCheck{ModelCall: call, SourceRef: fmt.Sprintf("u%d", index+1), QuoteHash: policyHash(quote),
			QuoteRunes: utf8.RuneCountInString(quote), FullPresent: present(quote), ExcerptRunes: utf8.RuneCountInString(excerpt), ExcerptPresent: present(excerpt)})
	}
	return checks
}

func targetRegressionError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func targetRegressionWriteJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	_, writeErr := file.Write(append(data, '\n'))
	return errors.Join(writeErr, file.Close())
}
