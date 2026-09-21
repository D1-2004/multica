package wsfs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Entry is one catalog row in the workbench shared disk.
type Entry struct {
	ID         uuid.UUID
	ParentPath string
	Name       string
	RelPath    string
	IsDir      bool
	SizeBytes  int64
	SHA256     string
	StorageKey string
	ModTime    time.Time
}

func ParentPath(rel string) string {
	if rel == "" || rel == "." {
		return ""
	}
	i := strings.LastIndex(rel, "/")
	if i < 0 {
		return ""
	}
	return rel[:i]
}

func JoinRel(parent, name string) string {
	if parent == "" || parent == "." {
		return name
	}
	return parent + "/" + name
}

func CatalogParentKey(rel string) string {
	if rel == "" || rel == "." {
		return ""
	}
	return rel
}

func agentArg(agentID uuid.UUID) any {
	if agentID == uuid.Nil {
		return nil
	}
	return agentID
}

func (s Store) ListChildren(ctx context.Context, workspaceID, agentID uuid.UUID, parent string) ([]DirEntry, error) {
	parent = CatalogParentKey(parent)
	rows, err := s.DB.Query(ctx, `SELECT name, rel_path, is_dir, size_bytes, sha256, updated_at
 FROM workspace_fs_entry WHERE workspace_id=$1 AND parent_path=$2 AND agent_id IS NOT DISTINCT FROM $3`,
		workspaceID, parent, agentArg(agentID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DirEntry
	for rows.Next() {
		var e DirEntry
		var mod time.Time
		if err := rows.Scan(&e.Name, &e.Path, &e.IsDir, &e.Size, &e.SHA256, &mod); err != nil {
			return nil, err
		}
		e.ModTime = mod.UTC().Format(time.RFC3339)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s Store) GetEntry(ctx context.Context, workspaceID, agentID uuid.UUID, rel string) (Entry, error) {
	if rel == "" || rel == "." {
		return Entry{IsDir: true, RelPath: ""}, nil
	}
	var e Entry
	err := s.DB.QueryRow(ctx, `SELECT id, parent_path, name, rel_path, is_dir, size_bytes, sha256, storage_key, updated_at
 FROM workspace_fs_entry WHERE workspace_id=$1 AND rel_path=$2 AND agent_id IS NOT DISTINCT FROM $3`,
		workspaceID, rel, agentArg(agentID)).Scan(
		&e.ID, &e.ParentPath, &e.Name, &e.RelPath, &e.IsDir, &e.SizeBytes, &e.SHA256, &e.StorageKey, &e.ModTime)
	return e, err
}

func (s Store) DirExists(ctx context.Context, workspaceID, agentID uuid.UUID, rel string) (bool, error) {
	if rel == "" || rel == "." {
		return true, nil
	}
	e, err := s.GetEntry(ctx, workspaceID, agentID, rel)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return e.IsDir, nil
}

func (s Store) ListAll(ctx context.Context, workspaceID, agentID uuid.UUID) ([]DirEntry, error) {
	rows, err := s.DB.Query(ctx, `SELECT name, rel_path, is_dir, size_bytes, sha256, updated_at
 FROM workspace_fs_entry WHERE workspace_id=$1 AND agent_id IS NOT DISTINCT FROM $2 ORDER BY rel_path`,
		workspaceID, agentArg(agentID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DirEntry
	for rows.Next() {
		var e DirEntry
		var mod time.Time
		if err := rows.Scan(&e.Name, &e.Path, &e.IsDir, &e.Size, &e.SHA256, &mod); err != nil {
			return nil, err
		}
		e.ModTime = mod.UTC().Format(time.RFC3339)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s Store) ListFiles(ctx context.Context, workspaceID uuid.UUID) ([]Entry, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, parent_path, name, rel_path, is_dir, size_bytes, sha256, storage_key, updated_at
 FROM workspace_fs_entry WHERE workspace_id=$1 AND is_dir=false AND agent_id IS NULL ORDER BY rel_path`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.ParentPath, &e.Name, &e.RelPath, &e.IsDir, &e.SizeBytes, &e.SHA256, &e.StorageKey, &e.ModTime); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s Store) InsertDir(ctx context.Context, workspaceID, agentID, createdBy uuid.UUID, rel, name string) (Entry, error) {
	parent := ParentPath(rel)
	id := uuid.New()
	var e Entry
	err := s.DB.QueryRow(ctx, `INSERT INTO workspace_fs_entry
 (id, workspace_id, agent_id, parent_path, name, rel_path, is_dir, created_by)
 VALUES ($1,$2,$3,$4,$5,$6,true,$7)
 RETURNING id, parent_path, name, rel_path, is_dir, size_bytes, sha256, storage_key, updated_at`,
		id, workspaceID, agentArg(agentID), parent, name, rel, createdBy).Scan(
		&e.ID, &e.ParentPath, &e.Name, &e.RelPath, &e.IsDir, &e.SizeBytes, &e.SHA256, &e.StorageKey, &e.ModTime)
	return e, err
}

func (s Store) InsertFile(ctx context.Context, id, workspaceID, agentID, createdBy uuid.UUID, rel, name, storageKey, sha256 string, size int64) (Entry, error) {
	parent := ParentPath(rel)
	var e Entry
	err := s.DB.QueryRow(ctx, `INSERT INTO workspace_fs_entry
 (id, workspace_id, agent_id, parent_path, name, rel_path, is_dir, size_bytes, sha256, storage_key, created_by)
 VALUES ($1,$2,$3,$4,$5,$6,false,$7,$8,$9,$10)
 RETURNING id, parent_path, name, rel_path, is_dir, size_bytes, sha256, storage_key, updated_at`,
		id, workspaceID, agentArg(agentID), parent, name, rel, size, sha256, storageKey, createdBy).Scan(
		&e.ID, &e.ParentPath, &e.Name, &e.RelPath, &e.IsDir, &e.SizeBytes, &e.SHA256, &e.StorageKey, &e.ModTime)
	return e, err
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s Store) EnsureParents(ctx context.Context, workspaceID, agentID, createdBy uuid.UUID, rel string) error {
	if rel == "" || rel == "." {
		return nil
	}
	acc := ""
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." {
			continue
		}
		acc = JoinRel(acc, part)
		exists, err := s.DirExists(ctx, workspaceID, agentID, acc)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.InsertDir(ctx, workspaceID, agentID, createdBy, acc, part); err != nil && !isUnique(err) {
			return err
		}
	}
	return nil
}

func (s Store) Rename(ctx context.Context, workspaceID, agentID uuid.UUID, oldRel, newRel, newName string) error {
	_, err := s.DB.Exec(ctx, `UPDATE workspace_fs_entry SET
 rel_path = CASE WHEN rel_path = $3 THEN $4 ELSE $4 || substr(rel_path, char_length($3)+1) END,
 parent_path = CASE
   WHEN parent_path = $3 THEN $4
   WHEN parent_path LIKE $3 || '/%' THEN $4 || substr(parent_path, char_length($3)+1)
   ELSE parent_path
 END,
 name = CASE WHEN rel_path = $3 THEN $5 ELSE name END,
 updated_at = now()
 WHERE workspace_id=$1 AND agent_id IS NOT DISTINCT FROM $2
 AND (rel_path = $3 OR rel_path LIKE $3 || '/%')`,
		workspaceID, agentArg(agentID), oldRel, newRel, newName)
	return err
}

func (s Store) StorageKeysUnder(ctx context.Context, workspaceID, agentID uuid.UUID, rel string) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT storage_key FROM workspace_fs_entry
 WHERE workspace_id=$1 AND agent_id IS NOT DISTINCT FROM $2 AND storage_key <> ''
 AND (rel_path = $3 OR rel_path LIKE $3 || '/%')`,
		workspaceID, agentArg(agentID), rel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s Store) DeleteUnder(ctx context.Context, workspaceID, agentID uuid.UUID, rel string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM workspace_fs_entry
 WHERE workspace_id=$1 AND agent_id IS NOT DISTINCT FROM $2
 AND (rel_path = $3 OR rel_path LIKE $3 || '/%')`,
		workspaceID, agentArg(agentID), rel)
	return err
}
