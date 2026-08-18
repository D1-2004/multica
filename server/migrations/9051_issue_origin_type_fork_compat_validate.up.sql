-- Validate the canonical constraint in a separate migration so the ACCESS
-- EXCLUSIVE lock taken by migration 9050 is released before this table scan.
ALTER TABLE issue VALIDATE CONSTRAINT issue_origin_type_check;
