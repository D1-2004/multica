package dws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A custom stream ticket names the token's app and its secret (the dws
// CLI's custom mode); the default stays a normal ticket without them.
func TestTicketModeNamesTheApp(t *testing.T) {
	for _, tt := range []struct {
		mode   string
		secret string
		want   map[string]string
	}{
		{mode: "", secret: "s3cret", want: map[string]string{"sourceId": "open", "mode": "normal"}},
		{mode: "custom", secret: "s3cret", want: map[string]string{"sourceId": "open", "mode": "custom", "clientId": "client-1", "clientSecret": "s3cret"}},
	} {
		var body map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{"success":true,"result":{"endpoint":"wss://stream.example/connect","ticket":"t-1"}}`))
		}))
		c, err := NewWithToken(context.Background(), Config{AuthURL: srv.URL, GatewayURL: srv.URL, SkipVerify: true,
			ClientSecret: tt.secret, StreamTicketMode: tt.mode}, Token{AccessToken: "uat", ClientID: "client-1"})
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := c.Events.Ticket(context.Background())
		srv.Close()
		if err != nil || ticket.Ticket != "t-1" {
			t.Fatalf("mode %q: ticket = %+v, %v", tt.mode, ticket, err)
		}
		if len(body) != len(tt.want) {
			t.Fatalf("mode %q: body = %v", tt.mode, body)
		}
		for k, v := range tt.want {
			if body[k] != v {
				t.Fatalf("mode %q: body[%s] = %q, want %q", tt.mode, k, body[k], v)
			}
		}
	}
	// Custom mode without a secret cannot name the app.
	c, err := NewWithToken(context.Background(), Config{AuthURL: "http://127.0.0.1:1", GatewayURL: "http://127.0.0.1:1",
		SkipVerify: true, StreamTicketMode: "custom"}, Token{AccessToken: "uat", ClientID: "client-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Events.Ticket(context.Background()); err == nil {
		t.Fatal("a custom ticket without the app secret was requested")
	}
}
