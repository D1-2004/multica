-- Irreversible compatibility cleanup. Recreating the retired index would
-- reject valid rows under the current cloud sandbox session identity model.
SELECT 1;
