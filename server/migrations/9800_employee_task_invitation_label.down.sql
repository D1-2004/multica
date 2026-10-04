DO $$
BEGIN
    IF to_regclass('employee_task_invitation') IS NOT NULL THEN
        ALTER TABLE employee_task_invitation DROP CONSTRAINT IF EXISTS employee_task_invitation_label_check;
        ALTER TABLE employee_task_invitation DROP COLUMN IF EXISTS participant_label;
    END IF;
END $$;
