CREATE TABLE IF NOT EXISTS workspace_fs_entry (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    parent_path text NOT NULL,
    name text NOT NULL,
    rel_path text NOT NULL,
    is_dir boolean NOT NULL,
    size_bytes bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    sha256 text NOT NULL DEFAULT '',
    storage_key text NOT NULL DEFAULT '',
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (name <> '' AND name <> '.' AND name <> '..' AND position('/' in name) = 0),
    CHECK (char_length(rel_path) <= 4096 AND char_length(parent_path) <= 4096),
    CHECK ((is_dir AND storage_key = '' AND size_bytes = 0) OR (NOT is_dir AND storage_key <> ''))
);
