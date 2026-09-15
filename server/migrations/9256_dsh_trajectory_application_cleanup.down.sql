-- A binary rollback must retain application-owned relationship cleanup.
-- Never recreate a foreign key or cascading action.
SELECT 1;
