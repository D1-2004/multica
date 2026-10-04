package evalreport

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	testWorkspace = "95391bf8-57f7-48cf-9959-91b801cd03a3"
	testUser      = "bc0e1298-2a0e-46a2-9070-dd696e0fc4ba"
	testReport    = "6a154157-96a2-43ed-9005-f4904912a8b3"
)

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("fake scan arity")
	}
	for i, value := range r.values {
		target := reflect.ValueOf(dest[i]).Elem()
		target.Set(reflect.ValueOf(value))
	}
	return nil
}

type fakeRows struct {
	pgx.Rows
	values   [][]any
	position int
	err      error
	closed   bool
}

func (r *fakeRows) Next() bool {
	if r.position >= len(r.values) {
		return false
	}
	r.position++
	return true
}
func (r *fakeRows) Scan(dest ...any) error {
	return (fakeRow{values: r.values[r.position-1]}).Scan(dest...)
}
func (r *fakeRows) Close()     { r.closed = true }
func (r *fakeRows) Err() error { return r.err }

type fakeDB struct {
	row    func(string, []any) pgx.Row
	query  func(string, []any) (pgx.Rows, error)
	tx     *fakeTx
	begins int
}

func (d *fakeDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	return d.row(query, args)
}
func (d *fakeDB) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	return d.query(query, args)
}
func (d *fakeDB) Begin(context.Context) (pgx.Tx, error) { d.begins++; return d.tx, nil }

type fakeTx struct {
	pgx.Tx
	row        func(string, []any) pgx.Row
	committed  bool
	rolledBack bool
}

func (tx *fakeTx) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	return tx.row(query, args)
}
func (tx *fakeTx) Commit(context.Context) error   { tx.committed = true; return nil }
func (tx *fakeTx) Rollback(context.Context) error { tx.rolledBack = true; return nil }

func storedRow(t *testing.T, hash string, metadata bool) []any {
	t.Helper()
	in, summary, actualHash, definitionHash, err := Normalize(exampleSubmission())
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" {
		hash = actualHash
	}
	if metadata {
		in.SelectedCases = nil
		in.Results = nil
	}
	submissionJSON, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	return []any{testReport, testWorkspace, "测试项目", testUser, in.FinishedAt.Add(time.Minute), hash, definitionHash, submissionJSON, summaryJSON}
}

func TestSubmitValidatesBeforeStorageAndNeverAcceptsSpoofedAuthority(t *testing.T) {
	db := &fakeDB{}
	store := NewStore(db, db)
	in := exampleSubmission()
	in.Results = nil
	if _, _, err := store.Submit(context.Background(), testWorkspace, testUser, in); !errors.Is(err, ErrInvalid) || db.begins != 0 {
		t.Fatalf("invalid body reached storage: %v", err)
	}
	if _, _, err := store.Submit(context.Background(), "not-a-workspace", testUser, exampleSubmission()); !errors.Is(err, ErrNotFound) || db.begins != 0 {
		t.Fatalf("untrusted scope reached storage: %v", err)
	}
	data := encoded(t, exampleSubmission())
	spoofed := append([]byte(`{"submitted_by":"95391bf8-57f7-48cf-9959-91b801cd03a3",`), data[1:]...)
	if _, err := Decode(spoofed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("body principal accepted: %v", err)
	}
}

func TestSubmitLocksMembershipBeforeInsertAndAssignsAuthenticatedSubmitter(t *testing.T) {
	tx := &fakeTx{}
	step := 0
	tx.row = func(query string, args []any) pgx.Row {
		step++
		switch step {
		case 1:
			if query != lockAccessSQL || !strings.Contains(query, "FOR KEY SHARE OF w FOR SHARE OF m") || args[0] != testWorkspace || args[1] != testUser {
				t.Fatalf("missing access/deletion locks: %s %v", query, args)
			}
			return fakeRow{values: []any{"测试项目"}}
		case 2:
			if !strings.Contains(query, "ON CONFLICT (workspace_id,run_id) DO NOTHING") || strings.Contains(query, "DO UPDATE") || args[1] != testWorkspace || args[2] != testUser {
				t.Fatalf("unsafe insert: %s", query)
			}
			var saved Submission
			if json.Unmarshal([]byte(args[6].(string)), &saved) != nil || saved.SelectedCases[0].ID != "G01" || saved.StartedAt.Location() != time.UTC {
				t.Fatal("unnormalized report saved")
			}
			return fakeRow{values: []any{time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)}}
		default:
			t.Fatal("unexpected SQL statement")
			return fakeRow{err: pgx.ErrNoRows}
		}
	}
	db := &fakeDB{tx: tx}
	record, replayed, err := NewStore(db, db).Submit(context.Background(), testWorkspace, testUser, exampleSubmission())
	if err != nil || replayed || record.SubmittedBy != testUser || !tx.committed || step != 2 {
		t.Fatalf("submit: %+v replay=%v err=%v", record, replayed, err)
	}
}

func TestIdenticalReplayReturnsOriginalReportAndDifferentContentConflicts(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "different"}[conflict], func(t *testing.T) {
			tx := &fakeTx{}
			step := 0
			tx.row = func(query string, args []any) pgx.Row {
				step++
				switch step {
				case 1:
					return fakeRow{values: []any{"测试项目"}}
				case 2:
					return fakeRow{err: pgx.ErrNoRows}
				case 3:
					if !strings.Contains(query, "m.user_id=$2::uuid") || !strings.Contains(query, "r.workspace_id=$1::uuid") || !strings.Contains(query, "r.run_id=$3::uuid") {
						t.Fatal("replay bypassed current membership")
					}
					hash := ""
					if conflict {
						hash = strings.Repeat("0", 64)
					}
					return fakeRow{values: storedRow(t, hash, false)}
				default:
					t.Fatal("unexpected replay SQL")
					return fakeRow{err: pgx.ErrNoRows}
				}
			}
			db := &fakeDB{tx: tx}
			record, replayed, err := NewStore(db, db).Submit(context.Background(), testWorkspace, testUser, exampleSubmission())
			if conflict {
				if !errors.Is(err, ErrConflict) || tx.committed {
					t.Fatalf("conflict overwrote report: %v", err)
				}
			} else if err != nil || !replayed || record.ID != testReport || !tx.committed {
				t.Fatalf("identical replay did not preserve report: %+v %v", record, err)
			}
		})
	}
}

func TestRevokedMembershipPreventsInsertAndAllReadsRecheckMembership(t *testing.T) {
	tx := &fakeTx{row: func(query string, args []any) pgx.Row {
		if query != lockAccessSQL {
			t.Fatal("revoked member reached INSERT")
		}
		return fakeRow{err: pgx.ErrNoRows}
	}}
	db := &fakeDB{tx: tx}
	if _, _, err := NewStore(db, db).Submit(context.Background(), testWorkspace, testUser, exampleSubmission()); !errors.Is(err, ErrNotFound) || tx.committed {
		t.Fatalf("revoked submit: %v", err)
	}
	db.row = func(query string, args []any) pgx.Row {
		if !strings.Contains(query, "m.user_id=$2::uuid") || args[0] != testWorkspace || args[1] != testUser {
			t.Fatalf("scoped read lacks current member: %s", query)
		}
		return fakeRow{err: pgx.ErrNoRows}
	}
	if _, err := NewStore(db, db).Get(context.Background(), testWorkspace, testUser, testReport); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked read: %v", err)
	}
	if _, _, err := NewStore(db, db).List(context.Background(), testWorkspace, testUser, "", 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked list: %v", err)
	}
}

func TestListAndVisiblePageOnlyReadBoundedMetadata(t *testing.T) {
	rows := &fakeRows{values: [][]any{storedRow(t, "", true), storedRow(t, "", true)}}
	db := &fakeDB{}
	db.row = func(query string, args []any) pgx.Row { return fakeRow{values: []any{true}} }
	db.query = func(query string, args []any) (pgx.Rows, error) {
		if strings.Contains(query, "r.submission,r.summary") || strings.Contains(query, "selected_cases") || strings.Contains(query, "results") || !strings.Contains(query, "jsonb_build_object") || !strings.Contains(query, "JOIN member m ON m.workspace_id=r.workspace_id") || args[len(args)-1] != 2 {
			t.Fatalf("unbounded/unscoped list SQL: %s", query)
		}
		return rows, nil
	}
	records, hasMore, err := NewStore(db, db).List(context.Background(), testWorkspace, testUser, "", 1)
	if err != nil || !hasMore || len(records) != 1 || !rows.closed || records[0].Submission.Title == "" || len(records[0].Submission.SelectedCases) != 0 {
		t.Fatalf("metadata list: %+v %v", records, err)
	}
	db.query = func(query string, args []any) (pgx.Rows, error) {
		if !strings.Contains(query, "WHERE m.user_id=$1::uuid") || args[0] != testUser || strings.Contains(query, "selected_cases") || !strings.Contains(query, "LIMIT $2") {
			t.Fatalf("unscoped/unbounded visible list: %s", query)
		}
		return &fakeRows{}, nil
	}
	records, err = NewStore(db, db).ListVisible(context.Background(), testUser, 10)
	if err != nil || records == nil || len(records) != 0 {
		t.Fatalf("empty visible list: %+v %v", records, err)
	}
}

func TestListCursorCannotCrossWorkspaceAndUsesStableTieBreak(t *testing.T) {
	rowCount := 0
	db := &fakeDB{}
	db.row = func(query string, args []any) pgx.Row {
		rowCount++
		if rowCount == 1 {
			return fakeRow{values: []any{true}}
		}
		if !strings.Contains(query, "r.workspace_id=$1::uuid") || !strings.Contains(query, "m.user_id=$2::uuid") || args[2] != testReport {
			t.Fatal("cursor not scoped by current membership/workspace")
		}
		return fakeRow{values: []any{time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)}}
	}
	db.query = func(query string, args []any) (pgx.Rows, error) {
		if !strings.Contains(query, "(r.received_at,r.id)<($3::timestamptz,$4::uuid)") || !strings.Contains(query, "ORDER BY r.received_at DESC,r.id DESC LIMIT $5") || args[3] != testReport {
			t.Fatalf("unstable cursor ordering: %s", query)
		}
		return &fakeRows{}, nil
	}
	if _, _, err := NewStore(db, db).List(context.Background(), testWorkspace, testUser, testReport, 20); err != nil {
		t.Fatal(err)
	}
	rowCount = 0
	db.row = func(query string, args []any) pgx.Row {
		rowCount++
		if rowCount == 1 {
			return fakeRow{values: []any{true}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}
	if _, _, err := NewStore(db, db).List(context.Background(), testWorkspace, testUser, testReport, 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invisible cursor was treated as empty page: %v", err)
	}
}

func TestStorageUnavailableDiffersFromAnEmptyListAndHidesDriverContent(t *testing.T) {
	var store *Store
	if _, err := store.ListVisible(context.Background(), testUser, 20); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil store: %v", err)
	}
	db := &fakeDB{row: func(string, []any) pgx.Row {
		return fakeRow{err: errors.New("driver rejected password=do-not-return-secret")}
	}}
	_, err := NewStore(db, nil).Get(context.Background(), testWorkspace, testUser, testReport)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "do-not-return-secret") {
		t.Fatalf("unsafe driver error: %v", err)
	}
}
