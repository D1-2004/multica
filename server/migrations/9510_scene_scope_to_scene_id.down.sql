-- Scene ids stay: rewriting them back to conversation ids would let an
-- older binary read configuration under a key the scene directory no longer
-- governs. Rolling back keeps scene-scope rows inert for the old binary.
SELECT 1;
