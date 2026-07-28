DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM fc_e2b_stable_release
        WHERE git_commit = '' OR acr_digest = ''
    ) THEN
        RAISE EXCEPTION 'cannot require manually supplied stable release evidence after derived releases exist';
    END IF;
END
$$;

ALTER TABLE fc_e2b_stable_release
    DROP COLUMN source_revision,
    DROP CONSTRAINT fc_e2b_stable_release_git_commit_check,
    DROP CONSTRAINT fc_e2b_stable_release_acr_digest_check,
    ALTER COLUMN git_commit DROP DEFAULT,
    ALTER COLUMN acr_digest DROP DEFAULT,
    ADD CONSTRAINT fc_e2b_stable_release_git_commit_check CHECK (
        git_commit ~ '^[0-9a-f]{40}$'
    ),
    ADD CONSTRAINT fc_e2b_stable_release_acr_digest_check CHECK (
        acr_digest ~ '^sha256:[0-9a-f]{64}$'
    );
