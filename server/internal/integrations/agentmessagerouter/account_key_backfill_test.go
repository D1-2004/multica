package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeAccountKeyBackfillStore struct {
	rows      []db.ChannelInstallation
	updated   int
	updateErr error
}

func (f *fakeAccountKeyBackfillStore) ListDingTalkAccountBindingAccountKeyBackfillRows(context.Context) ([]db.ChannelInstallation, error) {
	return append([]db.ChannelInstallation(nil), f.rows...), nil
}

func (f *fakeAccountKeyBackfillStore) BackfillDingTalkAccountRouterAccountKey(_ context.Context, params db.BackfillDingTalkAccountRouterAccountKeyParams) (db.ChannelInstallation, error) {
	if f.updateErr != nil {
		return db.ChannelInstallation{}, f.updateErr
	}
	for index, row := range f.rows {
		if row.ID != params.ID || string(row.Config) != string(params.ExpectedConfig) {
			continue
		}
		config, err := ParseDingTalkAccountConfig(row.Config)
		if err != nil || config.RouterSourceID != params.ExpectedRouterSourceID || config.RouterPlatform != "" ||
			config.RouterTenantID != "" || config.RouterAccountID != "" {
			return db.ChannelInstallation{}, pgx.ErrNoRows
		}
		config.RouterPlatform = params.RouterPlatform
		config.RouterTenantID = params.RouterTenantID
		config.RouterAccountID = params.RouterAccountID
		row.Config, err = config.Marshal()
		if err != nil {
			return db.ChannelInstallation{}, err
		}
		f.rows[index] = row
		f.updated++
		return row, nil
	}
	return db.ChannelInstallation{}, pgx.ErrNoRows
}

type fakeAccountKeyBackfillRouter struct {
	result DigitalEmployeeSourceIdentityResult
	err    error
	calls  [][]string
}

func (f *fakeAccountKeyBackfillRouter) GetDigitalEmployeeSourceIdentities(_ context.Context, sourceIDs []string) (DigitalEmployeeSourceIdentityResult, error) {
	f.calls = append(f.calls, append([]string(nil), sourceIDs...))
	return f.result, f.err
}

func TestAccountKeyBackfillDryRunApplyAndRepeat(t *testing.T) {
	row := accountKeyBackfillRowForTest(t)
	store := &fakeAccountKeyBackfillStore{rows: []db.ChannelInstallation{row}}
	router := &fakeAccountKeyBackfillRouter{result: DigitalEmployeeSourceIdentityResult{Sources: []DigitalEmployeeSourceIdentity{{
		SourceID: "source-legacy", Platform: "dingtalk", TenantID: "corp-a", AccountID: "employee-a",
		SourceType: "digital_employee", Domain: "channel",
	}}}}
	runner, err := NewAccountKeyBackfillRunner(store, router)
	if err != nil {
		t.Fatal(err)
	}

	dryRun, err := runner.Run(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Counts[AccountKeyBackfillWouldUpdate] != 1 || store.updated != 0 ||
		dryRun.Records[0].SourceFingerprint == "" || dryRun.Records[0].AccountFingerprint == "" {
		t.Fatalf("dry-run report=%#v updated=%d", dryRun, store.updated)
	}
	encoded, err := json.Marshal(dryRun)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"source-legacy", "corp-a", "employee-a", row.ID.String(), "Bearer secret-value"} {
		if strings.Contains(string(encoded), sensitive) {
			t.Fatalf("audit report leaked %q: %s", sensitive, encoded)
		}
	}
	apply, err := runner.Run(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if apply.Counts[AccountKeyBackfillUpdated] != 1 || store.updated != 1 {
		t.Fatalf("apply report=%#v updated=%d", apply, store.updated)
	}
	repeat, err := runner.Run(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Counts[AccountKeyBackfillAlreadyComplete] != 1 || store.updated != 1 {
		t.Fatalf("repeat report=%#v updated=%d", repeat, store.updated)
	}
}

func TestAccountKeyBackfillClassifiesUnavailableMissingTenantAndCASConflict(t *testing.T) {
	tests := []struct {
		name      string
		result    DigitalEmployeeSourceIdentityResult
		routerErr error
		storeErr  error
		want      AccountKeyBackfillOutcome
	}{
		{name: "unavailable", routerErr: errors.New("router unavailable"), want: AccountKeyBackfillRouterUnavailable},
		{name: "missing tenant", result: DigitalEmployeeSourceIdentityResult{Sources: []DigitalEmployeeSourceIdentity{{
			SourceID: "source-legacy", Platform: "dingtalk", AccountID: "employee-a", SourceType: "digital_employee", Domain: "channel",
		}}}, want: AccountKeyBackfillMissingTenant},
		{name: "CAS conflict", result: DigitalEmployeeSourceIdentityResult{Sources: []DigitalEmployeeSourceIdentity{{
			SourceID: "source-legacy", Platform: "dingtalk", TenantID: "corp-a", AccountID: "employee-a", SourceType: "digital_employee", Domain: "channel",
		}}}, storeErr: pgx.ErrNoRows, want: AccountKeyBackfillCASConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeAccountKeyBackfillStore{rows: []db.ChannelInstallation{accountKeyBackfillRowForTest(t)}, updateErr: tt.storeErr}
			router := &fakeAccountKeyBackfillRouter{result: tt.result, err: tt.routerErr}
			runner, err := NewAccountKeyBackfillRunner(store, router)
			if err != nil {
				t.Fatal(err)
			}
			report, err := runner.Run(context.Background(), true)
			if err != nil {
				t.Fatal(err)
			}
			if report.Counts[tt.want] != 1 || store.updated != 0 {
				t.Fatalf("report=%#v updated=%d", report, store.updated)
			}
		})
	}
}

func TestAccountKeyBackfillRealClientKeepsPerSourceFailuresIsolated(t *testing.T) {
	rows := make([]db.ChannelInstallation, 0, 3)
	for index, sourceID := range []string{"source-valid", "source-missing-tenant", "source-mismatch"} {
		row := accountKeyBackfillRowForTest(t)
		row.ID = mustUUIDForTest([]string{
			"11111111-1111-1111-1111-111111111111",
			"22222222-2222-2222-2222-222222222222",
			"33333333-3333-3333-3333-333333333333",
		}[index])
		config, err := ParseDingTalkAccountConfig(row.Config)
		if err != nil {
			t.Fatal(err)
		}
		config.RouterSourceID = sourceID
		row.Config, err = config.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/digital-employee-bindings/source-identities" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]any{"sources": []map[string]any{
				{"sourceId": "source-valid", "platform": "dingtalk", "tenantId": "corp-a", "accountId": "employee-a", "sourceType": "digital_employee", "domain": "channel"},
				{"sourceId": "source-missing-tenant", "platform": "dingtalk", "accountId": "employee-b", "sourceType": "digital_employee", "domain": "channel"},
				{"sourceId": "source-mismatch", "platform": "lark", "tenantId": "corp-c", "accountId": "employee-c", "sourceType": "digital_employee", "domain": "channel"},
			}, "missingSourceIds": []string{}},
		})
	}))
	defer server.Close()

	store := &fakeAccountKeyBackfillStore{rows: rows}
	runner, err := NewAccountKeyBackfillRunner(store, mustTestClient(t, server))
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts[AccountKeyBackfillWouldUpdate] != 1 ||
		report.Counts[AccountKeyBackfillMissingTenant] != 1 ||
		report.Counts[AccountKeyBackfillInvalidLocal] != 1 ||
		report.Counts[AccountKeyBackfillRouterUnavailable] != 0 {
		t.Fatalf("report=%#v", report)
	}
}

func TestAccountKeyBackfillDoesNotTreatCompleteAccountKeyWithoutChannelSourceAsComplete(t *testing.T) {
	row := accountKeyBackfillRowForTest(t)
	config, err := ParseDingTalkAccountConfig(row.Config)
	if err != nil {
		t.Fatal(err)
	}
	config.RouterSourceID = ""
	config.RouterPlatform = "dingtalk"
	config.RouterTenantID = "corp-a"
	config.RouterAccountID = "employee-a"
	row.Config, err = config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeAccountKeyBackfillStore{rows: []db.ChannelInstallation{row}}
	router := &fakeAccountKeyBackfillRouter{}
	runner, err := NewAccountKeyBackfillRunner(store, router)
	if err != nil {
		t.Fatal(err)
	}

	report, err := runner.Run(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts[AccountKeyBackfillInvalidLocal] != 1 || store.updated != 0 || len(router.calls) != 0 {
		t.Fatalf("report=%#v updated=%d router_calls=%#v", report, store.updated, router.calls)
	}
}

func accountKeyBackfillRowForTest(t *testing.T) db.ChannelInstallation {
	t.Helper()
	now := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	config.RouterSourceID = "source-legacy"
	config.BoundAt = &now
	store.row.Config, err = config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store.row.Status = "active"
	return store.row
}
