package wsfs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func (s Store) ListChildren(ctx context.Context, workspaceID uuid.UUID, parent string) ([]DirEntry, error) {
	parent = CatalogParentKey(parent)
	rows, err := s.DB.Query(ctx, `SELECT name, rel_path, is_dir, size_bytes, updated_at
 FROM workspace_fs_entry WHERE workspace_id=$1 AND parent_path=$2`, workspaceID, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DirEntry
	for rows.Next() {
		var e DirEntry
		var mod time.Time
		if err := rows.Scan(&e.Name, &e.Path, &e.IsDir, &e.Size, &mod); err != nil {
			return nil, err
		}
		e.ModTime = mod.UTC().Format(time.RFC3339)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s Store) GetEntry(ctx context.Context, workspaceID uuid.UUID, rel string) (Entry, error) {
	if rel == "" || rel == "." {
		return Entry{IsDir: true, RelPath: ""}, nil
	}
	var e Entry
	err := s.DB.QueryRow(ctx, `SELECT id, parent_path, name, rel_path, is_dir, size_bytes, sha256, storage_key, updated_at
 FROM workspace_fs_entry WHERE workspace_id=$1 AND rel_path=$2`, workspaceID, rel).Scan(
		&e.ID, &e.ParentPath, &e.Name, &e.RelPath, &e.IsDir, &e.SizeBytes, &e.SHA256, &e.StorageKey, &e.ModTime)
	return e, err
}

func (s Store) DirExists(ctx context.Context, workspaceID uuid.UUID, rel string) (bool, error) {
	if rel == "" || rel == "." {
		return true, nil
	}
	e, err := s.GetEntry(ctx, workspaceID, rel)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return e.IsDir, nil
}

func (s Store) InsertDir(ctx context.Context, workspaceID, createdBy uuid.UUID, rel, name string) (Entry, error) {
	parent := ParentPath(rel)
	id := uuid.New()
	var e Entry
	err := s.DB.QueryRow(ctx, `INSERT INTO workspace_fs_entry
 (id, workspace_id, parent_path, name, rel_path, is_dir, created_by)
 VALUES ($1,$2,$3,$4,$5,true,$6)
 RETURNING id, parent_path, name, rel_path, is_dir, size_bytes, sha256, storage_key, updated_at`,
		id, workspaceID, parent, name, rel, createdBy).Scan(
		&e.ID, &e.ParentPath, &e.Name, &e.RelPath, &e.IsDir, &e.SizeBytes, &e.SHA256, &e.StorageKey, &e.ModTime)
	return e, err
}

func (s Store) InsertFile(ctx context.Context, id, workspaceID, createdBy uuid.UUID, rel, name, storageKey, sha256 string, size int64) (Entry, error) {
	parent := ParentPath(rel)
	var e Entry
	err := s.DB.QueryRow(ctx, `INSERT INTO workspace_fs_entry
 (id, workspace_id, parent_path, name, rel_path, is_dir, size_bytes, sha256, storage_key, created_by)
 VALUES ($1,$2,$3,$4,$5,false,$6,$7,$8,$9)
 RETURNING id, parent_path, name, rel_path, is_dir, size_bytes, sha256, storage_key, updated_at`,
		id, workspaceID, parent, name, rel, size, sha256, storageKey, createdBy).Scan(
		&e.ID, &e.ParentPath, &e.Name, &e.RelPath, &e.IsDir, &e.SizeBytes, &e.SHA256, &e.StorageKey, &e.ModTime)
	return e, err
}
