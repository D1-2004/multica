package events

import (
	"context"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// Handler handles one identity's event; see Listener.Handle for the contract
// (persist before returning nil, idempotent on Event.ID).
type Handler func(ctx context.Context, identity string, ev Event) error

// Router sends each event to the handler registered for its key, else for
// its category (im, oa, voip, todo, card; see dws.EventDefinition), else
// to Default. Its Handle method fits Manager.Handle.
//
// Malformed events go to Default. Without a Default, an event nothing is
// registered for is acked unhandled: registering only some keys is an
// explicit choice to drop the rest.
type Router struct {
	Default    Handler
	keys       map[string]Handler
	categories map[string]Handler
}

// Key registers h for one event key.
func (r *Router) Key(key string, h Handler) *Router {
	if r.keys == nil {
		r.keys = map[string]Handler{}
	}
	r.keys[key] = h
	return r
}

// Category registers h for every key of a category.
func (r *Router) Category(category string, h Handler) *Router {
	if r.categories == nil {
		r.categories = map[string]Handler{}
	}
	r.categories[category] = h
	return r
}

// Handle dispatches ev.
func (r *Router) Handle(ctx context.Context, identity string, ev Event) error {
	if h := r.route(ev); h != nil {
		return h(ctx, identity, ev)
	}
	return nil
}

func (r *Router) route(ev Event) Handler {
	if ev.Malformed {
		return r.Default
	}
	if h, ok := r.keys[ev.Key]; ok {
		return h
	}
	if def, ok := dws.LookupEvent(ev.Key); ok {
		if h, ok := r.categories[def.Category]; ok {
			return h
		}
	}
	return r.Default
}
