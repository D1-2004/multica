package service

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

// WithTask binds a pre-resolved runtime overlay to the Task created in the
// caller's transaction. It performs no network I/O and cannot change authority.
func (p PreparedDirectTask) WithTask(task employeetask.Task) (PreparedDirectTask, error) {
	old := p.request.Task
	a, _ := json.Marshal(old.Definition)
	b, _ := json.Marshal(task.Definition)
	if task.ID == "" || task.Scope != old.Scope || task.OwnerLoop != old.OwnerLoop || task.DispatchMode != old.DispatchMode || task.RequesterRef != old.RequesterRef || string(a) != string(b) {
		return PreparedDirectTask{}, employeetask.ErrInvalid
	}
	p.request.Task = task
	raw, err := directTaskContext(p.request)
	if err != nil {
		return PreparedDirectTask{}, err
	}
	p.contextJSON = raw
	return p, nil
}
