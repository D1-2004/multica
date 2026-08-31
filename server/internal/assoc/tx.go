package assoc

import "context"

// TxRunner runs a Store callback in one transaction when the backing store
// supports it. Memory runs the callback directly.
type TxRunner interface {
	InTx(ctx context.Context, fn func(Store) error) error
}

func withStoreTx(ctx context.Context, store Store, fn func(Store) error) error {
	if tx, ok := store.(TxRunner); ok {
		return tx.InTx(ctx, fn)
	}
	return fn(store)
}

func (m *Memory) InTx(ctx context.Context, fn func(Store) error) error {
	return fn(m)
}
