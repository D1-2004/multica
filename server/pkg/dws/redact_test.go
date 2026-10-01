package dws

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Logging a Client, Pool, Config, Token or AuthCode never prints a secret.
func TestPrintingNeverLeaksSecrets(t *testing.T) {
	cfg := Config{SkipVerify: true, ClientSecret: "app-secret"}
	token := Token{AccessToken: "uat-SECRET", RefreshToken: "rt-SECRET", ClientID: "cid"}
	c, err := NewWithToken(context.Background(), cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	p := &Pool{Config: cfg}
	code := AuthCode{Code: "code-SECRET", ClientID: "cid"}
	for _, v := range []any{c, p, cfg, token, code, &token} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			out := fmt.Sprintf(verb, v)
			for _, secret := range []string{"uat-SECRET", "rt-SECRET", "app-secret", "code-SECRET"} {
				if strings.Contains(out, secret) {
					t.Errorf("%s of %T leaks %s: %s", verb, v, secret, out)
				}
			}
		}
	}
}

// A caller waiting behind another goroutine's mint gives up at its own
// deadline.
func TestPoolWaitRespectsTheCallersContext(t *testing.T) {
	release := make(chan struct{})
	p := &Pool{Config: Config{SkipVerify: true}, Mint: func(context.Context, string) (AuthCode, error) {
		<-release
		return AuthCode{}, fmt.Errorf("mint down")
	}}
	go func() { _, _ = p.Client(context.Background(), "agent-a") }()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := p.Client(ctx, "agent-a")
	close(release)
	var initErr *InitError
	if !asInit(err, &initErr) || initErr.Stage != StageMint || !initErr.Temporary || time.Since(started) > time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(started))
	}
}

func asInit(err error, target **InitError) bool {
	e, ok := err.(*InitError)
	if ok {
		*target = e
	}
	return ok
}

func TestParseSinceAcceptsRFC3339WithoutSeconds(t *testing.T) {
	got, err := ParseSince("2026-09-29T10:00+08:00", time.Now())
	if err != nil || !got.Equal(time.Date(2026, 9, 29, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %v, %v", got, err)
	}
}
