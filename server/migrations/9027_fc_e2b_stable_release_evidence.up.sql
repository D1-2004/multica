ALTER TABLE fc_e2b_stable_release
    DROP CONSTRAINT fc_e2b_stable_release_git_commit_check,
    DROP CONSTRAINT fc_e2b_stable_release_acr_digest_check,
    ALTER COLUMN git_commit SET DEFAULT '',
    ALTER COLUMN acr_digest SET DEFAULT '',
    ADD CONSTRAINT fc_e2b_stable_release_git_commit_check CHECK (
        git_commit = '' OR git_commit ~ '^[0-9a-f]{40}$'
    ),
    ADD CONSTRAINT fc_e2b_stable_release_acr_digest_check CHECK (
        acr_digest = '' OR acr_digest ~ '^sha256:[0-9a-f]{64}$'
    ),
    ADD COLUMN source_revision TEXT NOT NULL DEFAULT '' CHECK (
        source_revision = '' OR source_revision ~ '^[0-9a-f]{6}$'
    );
