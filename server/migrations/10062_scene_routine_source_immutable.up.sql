CREATE OR REPLACE FUNCTION protect_scene_routine_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.source IS DISTINCT FROM OLD.source THEN
   RAISE EXCEPTION 'scene routine source is immutable';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS context_scope_routine_source_immutable ON context_scope_routine;
CREATE TRIGGER context_scope_routine_source_immutable BEFORE UPDATE ON context_scope_routine
 FOR EACH ROW EXECUTE FUNCTION protect_scene_routine_source();
