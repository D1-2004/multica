package dwsclient

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
)

// consumeCardEventsSDK is ConsumeCardEvents through the SDK's event
// listener: one subscription to user_card_action_triggered for dir's
// identity, ready once the stream is connected, and each event handed over
// as the envelope userdecision.ParseEvent reads. The listener reconnects by
// itself; it stops with ctx, a consume error, or a session that can no
// longer refresh its token, so the owning service re-exchanges instead of
// reporting a consumer that will never receive another event.
func consumeCardEventsSDK(parent context.Context, client *dws.Client, session sdkSession, ready func(), consume func([]byte) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var consumerErr error
	var expired atomic.Bool
	listener := &dwsevents.Listener{
		Identity: "card-action:" + session.UID,
		// The listener asks for its client before every connection.
		Client: func(context.Context) (*dws.Client, error) {
			if !client.Alive() {
				expired.Store(true)
				cancel()
				return nil, dws.ErrSessionExpired
			}
			return client, nil
		},
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventCardAction}},
		Handle: func(_ context.Context, ev dwsevents.Event) error {
			if err := consume(EventLine(ev)); err != nil {
				consumerErr = err
				cancel()
				return err
			}
			return nil
		},
		Store: &readyStore{MemoryStore: &dwsevents.MemoryStore{}, ready: ready},
	}
	err := listener.Run(ctx)
	if consumerErr != nil {
		return consumerErr
	}
	if expired.Load() && parent.Err() == nil {
		return errors.New("DWS card consumer session expired")
	}
	return commandFailed(parent, "DWS card consumer stopped", err)
}

// EventLine is one event as the dws CLI's ndjson line (dws
// ProjectTransportOutput): the transport envelope with the frame data as a
// string, which carries payload.corpid for the decision parser. A frame
// that did not decode carries its redacted data the same way, so the
// consumer records it as undecodable like a CLI line it cannot parse.
func EventLine(ev dwsevents.Event) []byte {
	raw, _ := json.Marshal(struct {
		Type        string `json:"type"`
		EventID     string `json:"event_id"`
		CorpID      string `json:"event_corp_id,omitempty"`
		EventType   string `json:"event_type"`
		EventScope  string `json:"event_scope"`
		SubscribeID string `json:"subscribe_id,omitempty"`
		SourceID    string `json:"source_id"`
		RuleType    string `json:"rule_type,omitempty"`
		Data        string `json:"data"`
	}{"event", ev.ID, ev.CorpID, ev.Key, "personal", ev.SubscriptionID, dws.DefaultSourceID, ruleType(ev.Key), string(ev.Data)})
	return raw
}

// readyStore reports the first connected state, which the listener
// publishes only after the subscription was reconciled and the stream
// handshake succeeded: the CLI's "[event] ready".
type readyStore struct {
	*dwsevents.MemoryStore
	ready func()
	once  sync.Once
}

func (s *readyStore) SetStatus(ctx context.Context, st dwsevents.Status) error {
	if st.State == dwsevents.StateConnected {
		s.once.Do(s.ready)
	}
	return s.MemoryStore.SetStatus(ctx, st)
}

// decisionOrganizationSDK is the organization the exchange reported for the
// token, else the one the identity's profile names.
func decisionOrganizationSDK(ctx context.Context, client *dws.Client, session sdkSession) (string, error) {
	if session.CorpID != "" {
		return session.CorpID, nil
	}
	me, err := client.Contacts.Me(ctx)
	if err != nil {
		return "", decisionIdentityError("user_decision_sender_profile_lookup_failed")
	}
	if me.CorpID == "" {
		return "", decisionIdentityError("user_decision_sender_profile_invalid")
	}
	return me.CorpID, nil
}

func ruleType(key string) string {
	if def, ok := dws.LookupEvent(key); ok {
		return def.RuleType
	}
	return ""
}
