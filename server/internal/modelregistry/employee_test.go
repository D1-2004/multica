package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	openai "github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func employeeRegistry(t *testing.T) *Registry {
	t.Helper()
	dsn := os.Getenv("MODEL_REGISTRY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("MODEL_REGISTRY_TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "employee_models_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); admin.Close() })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, name := range []string{"9300_global_model_configuration", "9301_global_model_configuration_revision"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, "CREATE TABLE agent (model text)"); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return &Registry{Pool: pool, Box: box, Defaults: func() Snapshot {
		return Snapshot{Config: Config{Providers: []Provider{{ID: "mass", BaseURL: "https://diamond.example.test/v1", Models: []string{"default-model"}, Enabled: true, Builtin: true}}, AgentModels: []Ref{{"mass", "default-model"}}, DefaultModel: Ref{"mass", "default-model"}, Coordinator: []Ref{{"mass", "default-model"}}}, Keys: map[string]string{"mass": "DEFAULT_KEY_SENTINEL"}}
	}}
}
func employeeConfiguredRegistry(t *testing.T) (*Registry, Snapshot) {
	t.Helper()
	r := employeeRegistry(t)
	s, err := r.Save(context.Background(), Config{Providers: []Provider{{ID: "first", BaseURL: "https://first.example.test/v1", Models: []string{"model-a"}, Enabled: true, APIKey: "FIRST_KEY_SENTINEL"}, {ID: "second", BaseURL: "https://second.example.test/v1", Models: []string{"model-b"}, Enabled: true, APIKey: "SECOND_KEY_SENTINEL"}}, AgentModels: []Ref{{"mass", "default-model"}}, DefaultModel: Ref{"mass", "default-model"}, Coordinator: []Ref{{"first", "model-a"}, {"second", "model-b"}}, DiamondFallback: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return r, s
}

func TestEmployeeCoordinatorPlanUsesSameEffectiveChainWithoutSecrets(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint(configured), func(t *testing.T) {
			var r *Registry
			if configured {
				r, _ = employeeConfiguredRegistry(t)
			} else {
				r = employeeRegistry(t)
			}
			plan, err := r.CoordinatorPlan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			route, err := r.CoordinatorSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if plan.Version != 1 || plan.Revision != route.Snapshot.Config.Revision || !reflect.DeepEqual(plan.Candidates, route.Snapshot.Config.Coordinator) {
				t.Fatalf("different effective Coordinator chain: %+v %+v", plan, route.Snapshot.Config.Coordinator)
			}
			if configured && (len(plan.Candidates) != 3 || plan.Candidates[2] != (Ref{"mass", "default-model"})) {
				t.Fatal("Diamond fallback missing")
			}
			raw, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"KEY_SENTINEL", "https://", "api_key", "base_url"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("plan serialized credentials/config URL: %s", forbidden)
				}
			}
			var keys map[string]any
			_ = json.Unmarshal(raw, &keys)
			if len(keys) != 3 || keys["candidate_refs"] == nil {
				t.Fatalf("plan contains non-reference snapshot data: %s", raw)
			}
			original := plan.Candidates[0]
			plan.Candidates[0].Model = "changed-copy"
			next, err := r.CoordinatorPlan(context.Background())
			if err != nil || next.Candidates[0] != original {
				t.Fatal("plan mutated effective routing")
			}
		})
	}
}

func TestEmployeeCoordinatorAttemptUsesFrozenRefAndCurrentAuthorization(t *testing.T) {
	r, s := employeeConfiguredRegistry(t)
	ctx := context.Background()
	plan, err := r.CoordinatorPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config
	c.Coordinator = []Ref{{"second", "model-b"}}
	for i := range c.Providers {
		if c.Providers[i].ID == "first" {
			c.Providers[i].BaseURL = "https://rotated.example.test/v2"
			c.Providers[i].APIKey = "ROTATED_KEY_SENTINEL"
		}
	}
	s, err = r.Save(ctx, c, "rotation")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		if req.Header.Get("Authorization") != "Bearer ROTATED_KEY_SENTINEL" || req.URL.Path != "/v2/chat/completions" {
			t.Error("URL/key did not rotate as one snapshot")
		}
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		if body["model"] != "model-a" || body["enable_thinking"] != false {
			t.Error("model or request parameters changed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"model-a-version","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":31,"completion_tokens":7,"total_tokens":38}}`)
	}))
	defer server.Close()
	r.coordinatorTransport = employeeTestTransport(t, server, "rotated.example.test")
	attempt, err := r.PrepareCoordinatorAttempt(ctx, plan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Ref() != plan.Candidates[0] || attempt.ConfigurationRevision() != s.Config.Revision {
		t.Fatal("frozen candidate or current credential revision lost")
	}
	exporter := tracetest.NewInMemoryExporter()
	lf := langfuse.NewWithExporter(langfuse.Config{}, exporter)
	defer lf.Shutdown(ctx)
	trace := lf.StartTrace(ctx, langfuse.TraceOptions{Name: "employee_loop"})
	ctx = langfuse.ContextWithTrace(ctx, trace)
	params := openai.ChatCompletionNewParams{Model: "caller-model", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("unchanged message")}}
	params.SetExtraFields(map[string]any{"model": "extra-caller-model", "enable_thinking": false})
	out, err := attempt.Chat(ctx, params)
	trace.End(langfuse.EndOptions{})
	if err != nil || requests.Load() != 1 || out.Usage.PromptTokens != 31 || out.Usage.CompletionTokens != 7 || out.Model != "model-a-version" {
		t.Fatalf("attempt changed usage/model response or repeated HTTP: requests=%d err=%v out=%+v", requests.Load(), err, out)
	}
	if params.Model != "caller-model" || params.ExtraFields()["model"] != "extra-caller-model" {
		t.Fatal("attempt mutated caller request")
	}
	if len(exporter.GetSpans()) != 1 {
		t.Fatal("attempt emitted extra generation")
	}
	for _, mode := range []string{"disabled", "model_removed", "provider_removed"} {
		c = s.Config
		for i := range c.Providers {
			if c.Providers[i].ID == "first" {
				switch mode {
				case "disabled":
					c.Providers[i].Enabled = false
				case "model_removed":
					c.Providers[i].Enabled = true
					c.Providers[i].Models = []string{"other-model"}
				case "provider_removed":
					c.Providers = append(c.Providers[:i], c.Providers[i+1:]...)
				}
				break
			}
		}
		s, err = r.Save(context.Background(), c, mode)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = r.PrepareCoordinatorAttempt(context.Background(), plan, 0); !errors.Is(err, ErrCandidateUnavailable) {
			t.Fatalf("%s restored old authorization: %v", mode, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("preparing unavailable candidate made HTTP")
	}
}

type employeeTransport func(*http.Request) (*http.Response, error)

func (f employeeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func employeeTestTransport(t *testing.T, server *httptest.Server, expectedHost string) http.RoundTripper {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return employeeTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != expectedHost {
			t.Errorf("wrong validated credential origin: %s", request.URL.Host)
		}
		copy := request.Clone(request.Context())
		u := *request.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		copy.URL = &u
		copy.Host = target.Host
		return server.Client().Transport.RoundTrip(copy)
	})
}

func TestEmployeeCoordinatorAttemptHasOneHTTPAndNoFallback(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r, _ := employeeConfiguredRegistry(t)
			plan, err := r.CoordinatorPlan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				count.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"message":"provider busy","type":"provider_error"}}`)
			}))
			defer server.Close()
			r.coordinatorTransport = employeeTestTransport(t, server, "first.example.test")
			attempt, err := r.PrepareCoordinatorAttempt(context.Background(), plan, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = attempt.Chat(context.Background(), openai.ChatCompletionNewParams{Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")}}); err == nil || !Retryable(err) || count.Load() != 1 {
				t.Fatalf("SDK retry or fallback occurred: count=%d err=%v", count.Load(), err)
			}
		})
	}
}

func TestEmployeeCoordinatorAttemptMissingKeyAndUnsafeURLFailClosed(t *testing.T) {
	for _, mode := range []string{"key_removed", "unsafe_url"} {
		t.Run(mode, func(t *testing.T) {
			r := employeeRegistry(t)
			defaults := r.Defaults()
			plan, err := r.CoordinatorPlan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "key_removed" {
				defaults.Keys["mass"] = ""
				t.Setenv("OPENAI_API_KEY", "AMBIENT_MUST_NOT_BE_USED")
			} else {
				defaults.Config.Providers[0].BaseURL = "http://127.0.0.1/v1"
			}
			r.Defaults = func() Snapshot { return defaults }
			r.coordinatorTransport = employeeTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("unavailable candidate reached transport")
				return nil, nil
			})
			if _, err = r.PrepareCoordinatorAttempt(context.Background(), plan, 0); !errors.Is(err, ErrCandidateUnavailable) {
				t.Fatalf("unsafe candidate accepted: %v", err)
			}
		})
	}
}

func TestEmployeeCoordinatorAttemptRejectsInvalidPlanAndPreservesLoadErrors(t *testing.T) {
	r := employeeRegistry(t)
	ctx := context.Background()
	plan, err := r.CoordinatorPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := r.PrepareCoordinatorAttempt(ctx, plan, 0)
	if err != nil || attempt.Ref() != plan.Candidates[0] || attempt.ConfigurationRevision() != plan.Revision {
		t.Fatalf("default candidate differs from effective Coordinator: %v", err)
	}
	for _, tc := range []struct {
		plan  CoordinatorPlan
		index int
	}{{CoordinatorPlan{Version: 99, Candidates: plan.Candidates}, 0}, {plan, -1}, {plan, len(plan.Candidates)}} {
		if _, err = r.PrepareCoordinatorAttempt(ctx, tc.plan, tc.index); err == nil || errors.Is(err, ErrCandidateUnavailable) {
			t.Fatalf("invalid plan treated as a candidate fallback: %v", err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = r.PrepareCoordinatorAttempt(cancelled, plan, 0); !errors.Is(err, context.Canceled) || errors.Is(err, ErrCandidateUnavailable) {
		t.Fatalf("load cancellation lost: %v", err)
	}
	if _, err = r.Pool.Exec(ctx, "DROP TABLE global_model_configuration"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.PrepareCoordinatorAttempt(ctx, plan, 0); err == nil || errors.Is(err, ErrCandidateUnavailable) {
		t.Fatalf("database failure hidden as candidate unavailability: %v", err)
	}
}
