-- Fail closed if an existing workspace slug would collide with the new
-- workspace route segment. Resolve any conflict deliberately before deploy;
-- the application-level reserved-slug check prevents new ones after rollout.
DO $$
DECLARE
  conflict_count INT;
  conflict_list TEXT;
BEGIN
  SELECT COUNT(*), string_agg(slug, ', ' ORDER BY slug)
  INTO conflict_count, conflict_list
  FROM workspace
  WHERE slug = 'features';

  IF conflict_count > 0 THEN
    RAISE EXCEPTION 'Found % workspace(s) using the newly reserved slug features: %. Rename before deploying.', conflict_count, conflict_list;
  END IF;
END $$;
