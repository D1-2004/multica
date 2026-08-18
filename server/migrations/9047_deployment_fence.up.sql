-- A database-authoritative deployment fence for multi-replica rolling releases.
-- The ordinary state is normal. Operators first switch to draining, wait for
-- active work and durable completion outboxes to empty, then switch to frozen.

CREATE TABLE IF NOT EXISTS deployment_fence (
    singleton_id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (singleton_id = 1),
    state TEXT NOT NULL DEFAULT 'normal'
        CHECK (state IN ('normal', 'draining', 'frozen')),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    reason TEXT NOT NULL DEFAULT '',
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO deployment_fence (singleton_id, state, revision)
VALUES (1, 'normal', 1)
ON CONFLICT (singleton_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS deployment_fence_replica_ack (
    instance_id TEXT PRIMARY KEY CHECK (btrim(instance_id) <> ''),
    build_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('normal', 'draining', 'frozen')),
    revision BIGINT NOT NULL CHECK (revision > 0),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_deployment_fence_replica_ack_live
    ON deployment_fence_replica_ack (last_seen_at DESC);

CREATE OR REPLACE FUNCTION multica_current_deployment_fence_state()
RETURNS TEXT
LANGUAGE plpgsql
AS $$
DECLARE
    current_state TEXT;
BEGIN
    current_state := NULLIF(current_setting('multica.deployment_fence_state', true), '');
    IF current_state NOT IN ('normal', 'draining', 'frozen') THEN
        -- Every business transaction takes the shared side of this lock. A
        -- state transition takes the exclusive side, waits for prior writes,
        -- and prevents later writes from crossing the transition boundary.
        PERFORM pg_advisory_xact_lock_shared(7244554146635925502);
        SELECT fence.state
        INTO STRICT current_state
        FROM deployment_fence AS fence
        WHERE fence.singleton_id = 1;
        PERFORM set_config('multica.deployment_fence_state', current_state, true);
    END IF;
    RETURN current_state;
END;
$$;

CREATE OR REPLACE FUNCTION multica_enforce_deployment_fence()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF current_setting('multica.deployment_fence_bypass', true) = 'migration-runner' THEN
        RETURN NULL;
    END IF;
    IF multica_current_deployment_fence_state() = 'frozen' THEN
        RAISE EXCEPTION 'deployment fence is frozen'
            USING ERRCODE = '55000';
    END IF;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION multica_enforce_deployment_admission_fence()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    current_state TEXT;
BEGIN
    IF current_setting('multica.deployment_fence_bypass', true) = 'migration-runner' THEN
        RETURN NEW;
    END IF;
    current_state := multica_current_deployment_fence_state();
    IF current_state <> 'draining' THEN
        RETURN NEW;
    END IF;

    IF TG_TABLE_NAME = 'agent_task_queue' THEN
        IF TG_OP = 'INSERT' AND NEW.parent_task_id IS NULL THEN
            RAISE EXCEPTION 'deployment fence is draining; new root tasks are disabled'
                USING ERRCODE = '55000';
        END IF;
        IF TG_OP = 'UPDATE'
           AND NEW.parent_task_id IS NULL
           AND (
               (OLD.status = 'deferred' AND NEW.status = 'queued')
               OR (OLD.status = 'queued' AND NEW.status IN ('dispatched', 'running', 'waiting_local_directory'))
           ) THEN
            RAISE EXCEPTION 'deployment fence is draining; task admission is disabled'
                USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'deployment fence is draining; % admission is disabled', TG_TABLE_NAME
        USING ERRCODE = '55000';
END;
$$;

CREATE OR REPLACE FUNCTION multica_install_deployment_fence_triggers()
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    target RECORD;
    admission_table TEXT;
BEGIN
    FOR target IN
        SELECT namespace.nspname AS schema_name, class.relname AS table_name
        FROM pg_class AS class
        JOIN pg_namespace AS namespace ON namespace.oid = class.relnamespace
        WHERE namespace.nspname = 'public'
          AND class.relkind IN ('r', 'p')
          AND class.relname NOT IN (
              'schema_migrations',
              'deployment_fence',
              'deployment_fence_replica_ack'
          )
    LOOP
        EXECUTE format(
            'DROP TRIGGER IF EXISTS trg_deployment_fence_00_global ON %I.%I',
            target.schema_name,
            target.table_name
        );
        EXECUTE format(
            'CREATE TRIGGER trg_deployment_fence_00_global '
            'BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON %I.%I '
            'FOR EACH STATEMENT EXECUTE FUNCTION multica_enforce_deployment_fence()',
            target.schema_name,
            target.table_name
        );
    END LOOP;

    FOREACH admission_table IN ARRAY ARRAY[
        'agent_task_queue',
        'webhook_delivery',
        'dingtalk_stream_inbox',
        'channel_inbound_message_dedup',
        'autopilot_run',
        'agent_dispatch_acceptance'
    ]
    LOOP
        IF to_regclass('public.' || admission_table) IS NULL THEN
            CONTINUE;
        END IF;
        EXECUTE format(
            'DROP TRIGGER IF EXISTS trg_deployment_fence_10_admission ON public.%I',
            admission_table
        );
        IF admission_table = 'agent_task_queue' THEN
            EXECUTE format(
                'CREATE TRIGGER trg_deployment_fence_10_admission '
                'BEFORE INSERT OR UPDATE ON public.%I '
                'FOR EACH ROW EXECUTE FUNCTION multica_enforce_deployment_admission_fence()',
                admission_table
            );
        ELSE
            EXECUTE format(
                'CREATE TRIGGER trg_deployment_fence_10_admission '
                'BEFORE INSERT ON public.%I '
                'FOR EACH ROW EXECUTE FUNCTION multica_enforce_deployment_admission_fence()',
                admission_table
            );
        END IF;
    END LOOP;
END;
$$;

SELECT multica_install_deployment_fence_triggers();
