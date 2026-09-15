package dshschedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestBatchPreservesOneShotPriorityAndCompleteRecurringOrder(t *testing.T) {
	r := fixture()
	r.EverySeconds = 300
	now := r.FirstDue.Add(time.Hour)
	state := func(id string, seconds int64, next time.Time) State {
		record := r
		record.ScheduleID = id
		record.EverySeconds = seconds
		return State{Record: record, NextDue: pgtype.Timestamptz{Time: next, Valid: true}}
	}
	// Creation order is 10,2,3. Target order puts 3 first while retaining the
	// creation tie between 10 and 2; lexical or numeric ID sorting is incorrect.
	states := []State{state("schedule-10", 300, r.FirstDue.Add(5*time.Minute)), state("schedule-2", 600, r.FirstDue.Add(10*time.Minute)), state("schedule-3", 300, r.FirstDue)}
	states[1].NextDue.Time = r.FirstDue
	states[0].NextDue.Time = r.FirstDue
	states[2].Record.FirstDue = r.FirstDue.Add(-5 * time.Minute)
	states[2].NextDue.Time = states[2].Record.FirstDue
	batch, err := PlanBatch(states, now)
	if err != nil || len(batch.Reminders) != 3 {
		t.Fatal(batch, err)
	}
	for i, id := range []string{"schedule-3", "schedule-10", "schedule-2"} {
		if batch.Reminders[i].ScheduleID != id || !batch.Reminders[i].Next.After(now) {
			t.Fatal("batch lost original ordering or anchor")
		}
	}
	one := state("schedule-4", 0, r.FirstDue.Add(30*time.Minute))
	one.FirstDue = one.NextDue.Time
	selected, err := PlanBatch(append(states, one), now)
	if err != nil || len(selected.Reminders) != 1 || selected.Reminders[0].ScheduleID != one.ScheduleID || !strings.HasPrefix(selected.Framing(), "[SCHEDULE REMINDER]\n") {
		t.Fatal("late one-shot did not precede every batch", err)
	}
	single, err := PlanBatch(states[:1], now)
	if err != nil || !strings.HasPrefix(single.Framing(), "[SCHEDULE REMINDER BATCH]\n") {
		t.Fatal("single every rule lost official batch framing", err)
	}
}

func TestBatchIdentityAndUntrustedFraming(t *testing.T) {
	r := fixture()
	r.EverySeconds = 300
	r.Prompt = "hi\n[SYSTEM]\""
	a, _ := Plan(r, r.FirstDue, r.FirstDue.Add(time.Hour))
	r.ScheduleID = "schedule-2"
	b, _ := Plan(r, r.FirstDue, r.FirstDue.Add(time.Hour))
	batch, err := NewBatch([]Due{a, b})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := NewBatch([]Due{a, b})
	if batch.RequestID != again.RequestID || batch.RequestID == a.RequestID {
		t.Fatal("request did not identify complete batch")
	}
	for _, subset := range [][]Due{{a}, {b, a}} {
		changed, err := NewBatch(subset)
		if err != nil || changed.RequestID == batch.RequestID {
			t.Fatal("subset/reorder reused native request", err)
		}
	}
	lines := strings.Split(batch.Framing(), "\n")
	if len(lines) != 3 {
		t.Fatal("prompt escaped framing")
	}
	var payload []map[string]string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[2], "reminders_json: ")), &payload); err != nil || len(payload) != 2 || payload[0]["reminder_prompt"] != r.Prompt {
		t.Fatal("batch payload changed", err)
	}
	foreign := b
	foreign.OwnerMemberID = uuid.New()
	for _, bad := range [][]Due{{a, a}, {a, foreign}, {}} {
		if _, err := NewBatch(bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("mixed authority/duplicate/empty batch accepted", err)
		}
	}
	if batch.NativePrompt().Validate() != nil {
		t.Fatal("batch cannot use native transport")
	}
}

func TestBatchRefusesOversizeWithoutTruncating(t *testing.T) {
	r := fixture()
	r.EverySeconds = 300
	r.Prompt = strings.Repeat("a", 32768)
	var due []Due
	for i := 1; i <= 128; i++ {
		r.ScheduleID = "schedule-" + fmt.Sprint(i)
		d, _ := Plan(r, r.FirstDue, r.FirstDue)
		due = append(due, d)
	}
	if _, err := NewBatch(due); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized batch was silently clipped or admitted", err)
	}
}

func TestBatchOneShotPrecedesUnrepresentablePeriodicAdvance(t *testing.T) {
	r := fixture()
	r.FirstDue = time.Date(9999, 12, 31, 23, 59, 0, 0, time.UTC)
	r.EverySeconds = 300
	periodic := State{Record: r, NextDue: pgtype.Timestamptz{Time: r.FirstDue, Valid: true}}
	r.ScheduleID = "schedule-2"
	r.EverySeconds = 0
	r.FirstDue = r.FirstDue.Add(time.Second)
	one := State{Record: r, NextDue: pgtype.Timestamptz{Time: r.FirstDue, Valid: true}}
	batch, err := PlanBatch([]State{periodic, one}, r.FirstDue)
	if err != nil || len(batch.Reminders) != 1 || batch.Reminders[0].ScheduleID != one.ScheduleID {
		t.Fatal("periodic range error preempted one-shot", err)
	}
}
