package daemon

import (
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"
)

// taskMessageBuffer preserves arrival order and only combines adjacent fragments
// before their first send. A batch whose acknowledgement was lost is immutable.
type taskMessageBuffer struct {
	mu     sync.Mutex
	seq    *atomic.Int32
	batch  []TaskMessageData
	sealed int
}

func (b *taskMessageBuffer) append(m TaskMessageData) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n := len(b.batch); n > b.sealed && (m.Type == "text" || m.Type == "thinking") {
		last := &b.batch[n-1]
		var same bool
		if last.Event != nil && m.Event != nil {
			old, next := *last.Event, *m.Event
			old.ObservedAt = next.ObservedAt
			same = reflect.DeepEqual(old, next)
		} else {
			same = last.Event == nil && m.Event == nil
		}
		if last.Type == m.Type && same && len(last.Content)+len(m.Content) <= 64<<10 {
			last.Content += m.Content
			return
		}
	}
	m.Seq = int(b.seq.Add(1))
	b.batch = append(b.batch, m)
}

func (b *taskMessageBuffer) take() []TaskMessageData {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, bytes := 0, 0
	for n < len(b.batch) && n < 256 {
		raw, _ := json.Marshal(b.batch[n])
		if n > 0 && bytes+len(raw) > 4<<20 {
			break
		}
		bytes += len(raw)
		n++
	}
	batch := b.batch[:n:n]
	b.batch = b.batch[n:]
	b.sealed = max(0, b.sealed-n)
	return batch
}

func (b *taskMessageBuffer) retry(batch []TaskMessageData) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batch = append(batch, b.batch...)
	b.sealed += len(batch)
}
