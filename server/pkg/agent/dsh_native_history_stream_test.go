package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/dshtrajectory"
)

func nativeStreamFixture(t *testing.T, turns int, text string) (json.RawMessage, dshNativeCall) {
	t.Helper()
	page := func(turn int) []dshNativeRecord {
		request, output := "other", text
		if turn == 1 {
			request, output = "mine", "original result"
		}
		return nativeTestRecords(nativeTestTurn((turn-1)*4, turn, request, output, "completed"))
	}
	cursor, more := int64(turns*4-1), turns > 1
	raw, err := json.Marshal(dshNativeSnapshot{Type: "snapshot", Header: json.RawMessage(`{"version":3,"id":"session","cwd":"/work","isSeeded":false}`), Cursor: &cursor, Records: page(turns), HasMore: &more})
	if err != nil {
		t.Fatal(err)
	}
	return raw, func(_ context.Context, method string, request any) (json.RawMessage, error) {
		args := request.(map[string]any)
		before := args["beforeSeq"].(int64)
		if method != "page" || args["throughSeq"] != cursor || before%4 != 0 {
			t.Fatalf("wrong frozen cursor: %v", args)
		}
		turn := int(before / 4)
		more := turn > 1
		return json.Marshal(dshNativePage{Records: page(turn), HasMore: &more})
	}
}

func TestDSHNativeBaselineStreamsBeyondArtifactLimit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	raw, call := nativeStreamFixture(t, 35, strings.Repeat("x", 1<<20))
	h := dshNativeTaskHistory{requestID: "mine"}
	snapshot, err := walkDSHNativeBaseline(context.Background(), call, raw, "session", "/work", h.accept)
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.result("session")
	if err != nil || result.Output != "original result" || h.nextSeq != 140 || len(h.ownEvents) != 4 {
		t.Fatalf("lost original ownership: %+v %v", result, err)
	}
	if len(snapshot.Records) != 0 {
		t.Fatal("snapshot retains old page")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("spool leaked: %v %v", files, err)
	}
}

func TestDSHNativeHistoryDoesNotRetainLargeForeignTurn(t *testing.T) {
	for _, request := range []string{"other", "mine"} {
		t.Run(request, func(t *testing.T) {
			h := dshNativeTaskHistory{requestID: "mine"}
			for _, raw := range nativeTestTurn(0, 1, request, "", "completed")[:2] {
				if err := h.accept(raw); err != nil {
					t.Fatal(err)
				}
			}
			data := fmt.Sprintf(`{"text":%q}`, strings.Repeat("x", 1<<20))
			var failure error
			for i := 2; i < 36; i++ {
				failure = h.accept(nativeTestEvent(i, "tool/result", data))
				if failure != nil {
					break
				}
				if request == "other" && (len(h.pendingEvents) != 0 || len(h.ownEvents) != 0 || h.turnBytes != 0) {
					t.Fatal("foreign payload retained")
				}
			}
			if (failure != nil) != (request == "mine") {
				t.Fatalf("wrong per-task limit: %v", failure)
			}
			if request == "other" {
				if err := h.accept(nativeTestEvent(36, "turn/end", `{"turn":1,"reason":{"kind":"completed"}}`)); err != nil {
					t.Fatal(err)
				}
				for _, raw := range nativeTestTurn(37, 2, "mine", "new result", "completed") {
					if err := h.accept(raw); err != nil {
						t.Fatal(err)
					}
				}
				if len(h.ownEvents) != 4 {
					t.Fatal("foreign history leaked into task")
				}
			}
		})
	}
}

func TestDSHNativeBaselineFailureCleansSpoolAndDoesNotVisitPartialPrefix(t *testing.T) {
	for _, kind := range []string{"page failure", "gap", "cancel", "visitor failure", "disk unavailable"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			raw, call := nativeStreamFixture(t, 2, "fixture")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("test failure")
			visited := 0
			switch kind {
			case "page failure":
				call = func(context.Context, string, any) (json.RawMessage, error) { return nil, sentinel }
			case "gap":
				call = func(context.Context, string, any) (json.RawMessage, error) {
					return json.RawMessage(`{"records":[],"hasMore":false}`), nil
				}
			case "cancel":
				cancel()
			case "disk unavailable":
				t.Setenv("TMPDIR", dir+"/absent")
			}
			_, err := walkDSHNativeBaseline(ctx, call, raw, "session", "/work", func(json.RawMessage) error { visited++; return sentinel })
			if err == nil {
				t.Fatal("failure accepted")
			}
			if kind != "visitor failure" && visited != 0 {
				t.Fatal("partial prefix visited")
			}
			if kind == "visitor failure" && (!errors.Is(err, sentinel) || visited != 1) {
				t.Fatal("visitor failure lost")
			}
			files, readErr := os.ReadDir(dir)
			if readErr != nil || len(files) != 0 {
				t.Fatalf("spool leaked: %v %v", files, readErr)
			}
		})
	}
}

func TestDSHNativeChildCollectorBoundsOnlySelectedActivation(t *testing.T) {
	first := int64(1)
	c := dshChildRangeCollector{ref: dshtrajectory.ChildReference{FirstSeq: &first}}
	if err := c.accept(nativeTestEvent(0, "tool/result", fmt.Sprintf(`{"text":%q}`, strings.Repeat("x", 1<<20)))); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{
		nativeTestEvent(1, "multica/task-child-start", `{}`),
		nativeTestEvent(2, "tool/result", `{}`),
		nativeTestEvent(3, "multica/task-child-start", `{}`),
		nativeTestEvent(4, "tool/result", `{}`),
	} {
		if err := c.accept(raw); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.events) != 2 || c.lastSeq != 2 || c.closed {
		t.Fatal("interrupted range widened")
	}
	c = dshChildRangeCollector{ref: dshtrajectory.ChildReference{FirstSeq: &first}, bytes: dshtrajectory.MaxBytes}
	if err := c.accept(nativeTestEvent(1, "multica/task-child-start", `{}`)); err == nil {
		t.Fatal("oversized activation accepted")
	}
}
