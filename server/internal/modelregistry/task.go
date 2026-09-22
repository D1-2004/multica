package modelregistry

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// TaskSnapshot binds a task to its selected provider and encrypted credentials.
// Concurrent launch/first-request readers converge on the first committed snapshot.
func (r *Registry) TaskSnapshot(ctx context.Context, taskID, model string) (Snapshot, error) {
	var raw []byte
	err := r.Pool.QueryRow(ctx, `SELECT document FROM task_model_configuration WHERE task_id=$1`, taskID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		current, e := r.Load(ctx)
		if e != nil {
			return Snapshot{}, e
		}
		ref, e := current.AgentRef(model)
		if e != nil {
			return Snapshot{}, e
		}
		provider, key, e := current.Resolve(ref)
		if e != nil {
			return Snapshot{}, e
		}
		if r.Box == nil {
			return Snapshot{}, errors.New("provider encryption unavailable")
		}
		sealed, e := r.Box.Seal([]byte(key))
		if e != nil {
			return Snapshot{}, e
		}
		current.Config.Providers = []Provider{provider}
		current.Config.AgentModels = []Ref{ref}
		current.Config.DefaultModel = ref
		current.Config.Coordinator = nil
		raw, e = json.Marshal(stored{Config: current.Config, Secrets: map[string][]byte{ref.Provider: sealed}})
		if e != nil {
			return Snapshot{}, e
		}
		if _, e = r.Pool.Exec(ctx, `INSERT INTO task_model_configuration(task_id,document) VALUES($1,$2) ON CONFLICT(task_id) DO NOTHING`, taskID, raw); e != nil {
			return Snapshot{}, e
		}
		err = r.Pool.QueryRow(ctx, `SELECT document FROM task_model_configuration WHERE task_id=$1`, taskID).Scan(&raw)
	}
	if err != nil {
		return Snapshot{}, err
	}
	var saved stored
	if err = json.Unmarshal(raw, &saved); err != nil {
		return Snapshot{}, err
	}
	if r.Box == nil {
		return Snapshot{}, errors.New("provider encryption unavailable")
	}
	result := Snapshot{Config: saved.Config, Keys: map[string]string{}}
	for id, ciphertext := range saved.Secrets {
		plain, e := r.Box.Open(ciphertext)
		if e != nil {
			return Snapshot{}, e
		}
		result.Keys[id] = string(plain)
	}
	return result, nil
}
func (r *Registry) PrepareTaskModel(ctx context.Context, taskID, model string) (string, error) {
	s, e := r.TaskSnapshot(ctx, taskID, model)
	if e != nil {
		return "", e
	}
	return taskRuntimeModel(s.Config.DefaultModel), nil
}

// Builtin models keep their legacy runtime ID so existing native DSH catalogs
// can select them. The gateway resolves this alias against the frozen mass
// provider; custom providers remain qualified to avoid name collisions.
func taskRuntimeModel(ref Ref) string {
	if ref.Provider == "mass" {
		return ref.Model
	}
	return ref.String()
}
