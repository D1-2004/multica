CREATE TABLE IF NOT EXISTS workspace_filesystem (
    workspace_id uuid NOT NULL,
    file_system_id text NOT NULL CHECK (file_system_id <> ''),
    space_id text NOT NULL CHECK (space_id <> ''),
    vpc_id text NOT NULL,
    security_group_id text NOT NULL,
    vswitch_ids text[] NOT NULL,
    ro_access_point_arn text NOT NULL,
    rw_access_point_arn text NOT NULL,
    ro_role_arn text NOT NULL,
    rw_role_arn text NOT NULL,
    ro_volume_name text NOT NULL,
    rw_volume_name text NOT NULL,
    size_limit bigint NOT NULL,
    file_count_limit bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
