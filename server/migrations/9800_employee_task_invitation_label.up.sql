-- The participant's display name as the provider showed it when the
-- invitation was created, so the origin summary and views can name people
-- without exposing normalized identity refs. Older binaries never write it
-- and read it as the default.
--
-- This number sorts before 9920, which creates the table: on a fresh
-- database this is a no-op and 9920 creates the column itself; on a database
-- that already applied 9920 (pre-release) it adds the column. Idempotent.
DO $$
BEGIN
    IF to_regclass('employee_task_invitation') IS NOT NULL THEN
        ALTER TABLE employee_task_invitation
            ADD COLUMN IF NOT EXISTS participant_label text NOT NULL DEFAULT '';
        ALTER TABLE employee_task_invitation
            DROP CONSTRAINT IF EXISTS employee_task_invitation_label_check;
        ALTER TABLE employee_task_invitation
            ADD CONSTRAINT employee_task_invitation_label_check CHECK (char_length(participant_label) <= 128);
    END IF;
END $$;
