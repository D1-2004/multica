-- name: LockAgentPackageSkillEnabled :one
SELECT enabled FROM agent_skill
WHERE agent_id = $1 AND skill_id = $2
FOR UPDATE;
