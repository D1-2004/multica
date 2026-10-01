package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Message-level mutual exclusivity between the Agent Message Router and
// native DWS subscriptions. A DingTalk account (dws uid + org id) is owned by
// native subscription iff its agent_dws_native_subscription row exists, the
// row's agent is not archived, and that agent's current identity is still
// the account; otherwise the Router owns it. Both paths apply this one rule
// to every message, so a binding that slipped past the switch-level guards
// still never yields two deliveries, and an account is never owned by nobody.

// nativeOwnershipStore resolves account ownership; h.Queries in production.
type nativeOwnershipStore interface {
	GetDWSNativeAccountOwner(context.Context, db.GetDWSNativeAccountOwnerParams) (db.GetDWSNativeAccountOwnerRow, error)
}

// nativeAccountOwner returns the native owner of an account; owned is false
// when the Router owns it.
func nativeAccountOwner(ctx context.Context, store nativeOwnershipStore, uid, orgID string) (db.GetDWSNativeAccountOwnerRow, bool, error) {
	uid, orgID = strings.TrimSpace(uid), strings.TrimSpace(orgID)
	if store == nil || uid == "" || orgID == "" {
		return db.GetDWSNativeAccountOwnerRow{}, false, nil
	}
	owner, err := store.GetDWSNativeAccountOwner(ctx, db.GetDWSNativeAccountOwnerParams{OrgID: orgID, DwsUid: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.GetDWSNativeAccountOwnerRow{}, false, nil
	}
	if err != nil {
		return db.GetDWSNativeAccountOwnerRow{}, false, err
	}
	return owner, true, nil
}

func (h *Handler) nativeOwnership() nativeOwnershipStore {
	if h.dwsNativeOwnership != nil {
		return h.dwsNativeOwnership
	}
	if h.Queries != nil {
		return h.Queries
	}
	return nil
}

// dropNativeOwnedDelivery answers a Router digital-employee channel delivery
// for an account native subscription owns: nothing is recorded, accepted or
// replied, and the Router callback is closed silently so it does not retry.
// Robot, calendar and approval deliveries stay with the Router; native
// subscriptions receive only the account's IM messages.
func (h *Handler) dropNativeOwnedDelivery(w http.ResponseWriter, r *http.Request, command DispatchCommand, dispatchContext agentDispatchContext) bool {
	if isNativeDispatch(r.Context()) || command.Source.Type != "digital_employee" ||
		command.Event.Domain != "channel" || command.ExternalIdentity.DWS == nil {
		return false
	}
	owner, owned, err := nativeAccountOwner(r.Context(), h.nativeOwnership(), command.ExternalIdentity.DWS.UID, command.ExternalIdentity.DWS.OrgID)
	if err != nil {
		// Unknown ownership must not process twice; the Router retries.
		slog.Error("MULTICA_AGENT_DISPATCH_REQUEST", "outcome", "native_ownership_unavailable",
			"protocol", "dispatch_command_v2", "error", err)
		writeError(w, http.StatusServiceUnavailable, "failed to resolve the account's message owner")
		return true
	}
	if !owned {
		return false
	}
	slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
		"outcome", "skipped_native_owned",
		"protocol", "dispatch_command_v2",
		"eventType", command.Event.Type,
		"sourceType", command.Source.Type,
		"agentId", util.UUIDToString(dispatchContext.AgentID),
		"nativeAgentId", util.UUIDToString(owner.AgentID),
	)
	decision := inboundcoord.Decision{Action: inboundcoord.ActionSilence, Source: coordinatorSource(command)}
	inboundcoord.RecordDecision(r.Context(), decision)
	if writeDispatchCoordinatorTerminal(w, r.Context(), h, command, dispatchContext, decision) {
		return true
	}
	w.WriteHeader(http.StatusAccepted)
	return true
}

// nativeSourceRunning reports whether native events can arrive at all: the
// source exists (Redis) and runtime.use_dws_for_tag switches it on.
func (h *Handler) nativeSourceRunning() bool {
	if h.nativeSourceActive != nil {
		return h.nativeSourceActive()
	}
	return h.DWSNativeEvents != nil && h.DWSNativeEvents.Active()
}

// nativeFingerprintEvent is the event a native acceptance fingerprint covers.
// The quoted author is resolved from receipts that can appear between two
// deliveries of one message, so it stays out: a redelivery replays the first
// acceptance instead of conflicting with it.
func nativeFingerprintEvent(event DispatchEvent) DispatchEvent {
	messages := make([]DispatchMessage, len(event.Data.Messages))
	copy(messages, event.Data.Messages)
	for i := range messages {
		if ref := messages[i].ReferencedMessage; ref != nil {
			cleared := *ref
			cleared.SenderUID = ""
			messages[i].ReferencedMessage = &cleared
		}
	}
	event.Data.Messages = messages
	return event
}

const (
	nativeLoopLimit  = 10
	nativeLoopWindow = time.Minute
)

// nativeDispatchLoops breaks reply loops the other guards miss: more than
// nativeLoopLimit distinct native messages dispatched for one agent,
// conversation and sender within nativeLoopWindow. It is per process on purpose: a
// conversation's stream runs on one replica at a time, and losing the count
// on a handover only delays the breaker by a window.
var nativeDispatchLoops = newNativeLoopBreaker(nativeLoopLimit, nativeLoopWindow)

type nativeLoopEntry struct {
	at        time.Time
	messageID string
}

type nativeLoopBreaker struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string][]nativeLoopEntry
}

func newNativeLoopBreaker(limit int, window time.Duration) *nativeLoopBreaker {
	return &nativeLoopBreaker{limit: limit, window: window, entries: map[string][]nativeLoopEntry{}}
}

// admit records a dispatch of messageID under key and reports whether it is
// within the limit. A message already counted in the window is a redelivery
// and is admitted without counting again; a refused one is not recorded.
func (b *nativeLoopBreaker) admit(key, messageID string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.recent(b.entries[key], now)
	for _, entry := range kept {
		if entry.messageID == messageID {
			b.entries[key] = kept
			return true
		}
	}
	if len(kept) >= b.limit {
		b.entries[key] = kept
		return false
	}
	b.entries[key] = append(kept, nativeLoopEntry{at: now, messageID: messageID})
	if len(b.entries) > 4096 {
		for k, list := range b.entries {
			if recent := b.recent(list, now); len(recent) == 0 {
				delete(b.entries, k)
			} else {
				b.entries[k] = recent
			}
		}
	}
	return true
}

func (b *nativeLoopBreaker) recent(list []nativeLoopEntry, now time.Time) []nativeLoopEntry {
	kept := list[:0]
	for _, entry := range list {
		if now.Sub(entry.at) < b.window {
			kept = append(kept, entry)
		}
	}
	return kept
}
