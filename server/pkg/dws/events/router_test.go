package events

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/pkg/dws"
)

func TestRouterPrefersKeyThenCategoryThenDefault(t *testing.T) {
	var got []string
	record := func(name string) Handler {
		return func(context.Context, string, Event) error { got = append(got, name); return nil }
	}
	r := (&Router{Default: record("default")}).
		Key(dws.EventCardAction, record("card")).
		Category("im", record("im")).
		Category("card", record("card-category"))
	ctx := context.Background()
	for _, ev := range []Event{
		{Key: dws.EventCardAction},
		{Key: dws.EventIMGroup},
		{Key: dws.EventTodoCreated},
		{Key: "user_future_event"},
		{Key: dws.EventIMGroup, Malformed: true},
	} {
		if err := r.Handle(ctx, "agent-a", ev); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"card", "im", "default", "default", "default"}
	if len(got) != len(want) {
		t.Fatalf("routed %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("routed %v, want %v", got, want)
		}
	}
	// Without a Default, unregistered events are acked unhandled.
	if err := (&Router{}).Handle(ctx, "agent-a", Event{Key: dws.EventIMAt}); err != nil {
		t.Fatal(err)
	}
}
