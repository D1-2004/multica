package evalreport

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DB and Beginner can be injected separately from the existing handler interfaces.
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Store struct {
	db      DB
	starter Beginner
}

func NewStore(db DB, starter Beginner) *Store { return &Store{db: db, starter: starter} }

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && len(value) == 36
}

// lockAccess shares the workspace teardown and member-revocation locks. The
// authenticated principal is checked in SQL, never read from the submission.
const lockAccessSQL = `SELECT w.name FROM workspace w
	JOIN member m ON m.workspace_id=w.id
	WHERE w.id=$1::uuid AND m.user_id=$2::uuid
	FOR KEY SHARE OF w FOR SHARE OF m`

const recordColumns = `r.id::text,r.workspace_id::text,w.name,r.submitted_by::text,
	r.received_at,r.content_sha256,r.definition_sha256,r.submission,r.summary`

const metadataColumns = `r.id::text,r.workspace_id::text,w.name,r.submitted_by::text,
	r.received_at,r.content_sha256,r.definition_sha256,
	jsonb_build_object('schema_version',r.submission->'schema_version',
	'run_id',r.submission->'run_id','title',r.submission->'title',
	'execution_kind',r.submission->'execution_kind','environment',r.submission->'environment',
	'target_revision',r.submission->'target_revision','runner',r.submission->'runner',
	'started_at',r.submission->'started_at','finished_at',r.submission->'finished_at',
	'catalog',r.submission->'catalog'),r.summary`

const visibleJoins = ` FROM eval_report r
	JOIN workspace w ON w.id=r.workspace_id
	JOIN member m ON m.workspace_id=r.workspace_id`

type scanner interface{ Scan(...any) error }

func scanRecord(row scanner) (Record, error) {
	var record Record
	var submission, summary []byte
	if err := row.Scan(&record.ID, &record.WorkspaceID, &record.WorkspaceName, &record.SubmittedBy,
		&record.ReceivedAt, &record.ContentSHA256, &record.DefinitionSHA256, &submission, &summary); err != nil {
		return Record{}, storageError(err)
	}
	if json.Unmarshal(submission, &record.Submission) != nil || json.Unmarshal(summary, &record.Summary) != nil {
		return Record{}, ErrUnavailable
	}
	record.ReceivedAt = record.ReceivedAt.UTC()
	return record, nil
}

func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	// Driver errors can contain submitted values. Preserve only the public category.
	return ErrUnavailable
}

// Submit validates before beginning a transaction and saves the normalized report
// once. Concurrent replays use the unique workspace/run key, never an UPDATE.
func (s *Store) Submit(ctx context.Context, workspaceID, userID string, in Submission) (Record, bool, error) {
	if !validUUID(workspaceID) || !validUUID(userID) {
		return Record{}, false, ErrNotFound
	}
	normalized, summary, contentHash, definitionHash, err := Normalize(in)
	if err != nil {
		return Record{}, false, err
	}
	if s == nil || s.db == nil || s.starter == nil {
		return Record{}, false, ErrUnavailable
	}
	tx, err := s.starter.Begin(ctx)
	if err != nil {
		return Record{}, false, ErrUnavailable
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var workspaceName string
	if err := tx.QueryRow(ctx, lockAccessSQL, workspaceID, userID).Scan(&workspaceName); err != nil {
		return Record{}, false, storageError(err)
	}
	submissionJSON, _ := json.Marshal(normalized)
	summaryJSON, _ := json.Marshal(summary)
	record := Record{ID: uuid.NewString(), WorkspaceID: workspaceID, WorkspaceName: workspaceName,
		SubmittedBy: userID, ContentSHA256: contentHash, DefinitionSHA256: definitionHash,
		Submission: normalized, Summary: summary}
	err = tx.QueryRow(ctx, `INSERT INTO eval_report
		(id,workspace_id,submitted_by,run_id,content_sha256,definition_sha256,submission,summary)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7::jsonb,$8::jsonb)
		ON CONFLICT (workspace_id,run_id) DO NOTHING RETURNING received_at`,
		record.ID, workspaceID, userID, normalized.RunID, contentHash, definitionHash, string(submissionJSON), string(summaryJSON)).Scan(&record.ReceivedAt)
	replayed := false
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent insert has committed before ON CONFLICT returns; the next
		// Read Committed statement observes it. Membership is rechecked even here.
		record, err = scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+visibleJoins+
			` WHERE r.workspace_id=$1::uuid AND m.user_id=$2::uuid AND r.run_id=$3::uuid`,
			workspaceID, userID, normalized.RunID))
		if err != nil {
			return Record{}, false, err
		}
		if record.ContentSHA256 != contentHash {
			return Record{}, false, ErrConflict
		}
		replayed = true
	} else if err != nil {
		return Record{}, false, ErrUnavailable
	}
	if err := tx.Commit(ctx); err != nil {
		return Record{}, false, ErrUnavailable
	}
	record.ReceivedAt = record.ReceivedAt.UTC()
	return record, replayed, nil
}

// Get always scopes by the caller's current workspace membership.
func (s *Store) Get(ctx context.Context, workspaceID, userID, reportID string) (Record, error) {
	if !validUUID(workspaceID) || !validUUID(userID) || !validUUID(reportID) {
		return Record{}, ErrNotFound
	}
	if s == nil || s.db == nil {
		return Record{}, ErrUnavailable
	}
	return scanRecord(s.db.QueryRow(ctx, `SELECT `+recordColumns+visibleJoins+
		` WHERE r.workspace_id=$1::uuid AND m.user_id=$2::uuid AND r.id=$3::uuid`, workspaceID, userID, reportID))
}

// List returns bounded metadata, not large definition/result arrays. Its opaque
// report-ID cursor must itself be visible in the same workspace.
func (s *Store) List(ctx context.Context, workspaceID, userID, beforeID string, limit int) ([]Record, bool, error) {
	if !validUUID(workspaceID) || !validUUID(userID) {
		return nil, false, ErrNotFound
	}
	if limit < 1 || limit > 50 {
		return nil, false, invalid("limit", "must be between 1 and 50")
	}
	if beforeID != "" && !validUUID(beforeID) {
		return nil, false, invalid("before_id", "requires a report UUID")
	}
	if s == nil || s.db == nil {
		return nil, false, ErrUnavailable
	}
	var visible bool
	err := s.db.QueryRow(ctx, `SELECT true FROM workspace w JOIN member m ON m.workspace_id=w.id
		WHERE w.id=$1::uuid AND m.user_id=$2::uuid`, workspaceID, userID).Scan(&visible)
	if err != nil {
		return nil, false, storageError(err)
	}
	var beforeTime time.Time
	if beforeID != "" {
		err := s.db.QueryRow(ctx, `SELECT r.received_at`+visibleJoins+
			` WHERE r.workspace_id=$1::uuid AND m.user_id=$2::uuid AND r.id=$3::uuid`, workspaceID, userID, beforeID).Scan(&beforeTime)
		if err != nil {
			return nil, false, storageError(err)
		}
	}
	query := `SELECT ` + metadataColumns + visibleJoins + ` WHERE r.workspace_id=$1::uuid AND m.user_id=$2::uuid`
	args := []any{workspaceID, userID}
	if beforeID != "" {
		query += ` AND (r.received_at,r.id)<($3::timestamptz,$4::uuid)`
		args = append(args, beforeTime, beforeID)
	}
	// Fixed parameter positions avoid interpolating any client text into SQL.
	if beforeID == "" {
		query += ` ORDER BY r.received_at DESC,r.id DESC LIMIT $3`
	} else {
		query += ` ORDER BY r.received_at DESC,r.id DESC LIMIT $5`
	}
	args = append(args, limit+1)
	records, err := s.readMetadata(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	return records, hasMore, nil
}

// ListVisible is the pre-workspace page's bounded list. Every row is joined to
// the authenticated user's current membership, not submission ownership.
func (s *Store) ListVisible(ctx context.Context, userID string, limit int) ([]Record, error) {
	if !validUUID(userID) {
		return nil, ErrNotFound
	}
	if limit < 1 || limit > 50 {
		return nil, invalid("limit", "must be between 1 and 50")
	}
	if s == nil || s.db == nil {
		return nil, ErrUnavailable
	}
	return s.readMetadata(ctx, `SELECT `+metadataColumns+visibleJoins+
		` WHERE m.user_id=$1::uuid ORDER BY r.received_at DESC,r.id DESC LIMIT $2`, userID, limit)
}

func (s *Store) readMetadata(ctx context.Context, query string, args ...any) ([]Record, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	records := []Record{}
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return records, nil
}
