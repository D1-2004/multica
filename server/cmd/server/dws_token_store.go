package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/internal/util/secretbox"
	"github.com/multica-ai/multica/server/pkg/dws"
	"github.com/multica-ai/multica/server/pkg/dws/redisstore"
)

// dwsTokenStore is where replicas share DWS identities' tokens (the SDK
// transport, runtime.use_dws_for_tag): the store Redis, every token sealed
// with MULTICA_DINGTALK_SECRET_KEY, which every replica holds. Keys carry
// the deployment's public URL, so two deployments on one Redis never read
// each other's tokens (their agent ids can coincide). Without Redis or the
// key it returns nil and tokens stay per process.
func dwsTokenStore(rdb *redis.Client) dws.TokenStore {
	if rdb == nil {
		slog.Info("DWS tokens are per process: no Redis", "event", "dws_token_store_disabled", "reason", "no_redis")
		return nil
	}
	key, err := secretbox.LoadKey("MULTICA_DINGTALK_SECRET_KEY")
	if err != nil {
		slog.Info("DWS tokens are per process: no sealing key", "event", "dws_token_store_disabled", "reason", "no_key")
		return nil
	}
	box, err := secretbox.New(key)
	if err != nil {
		slog.Error("DWS tokens are per process: sealing key unusable", "event", "dws_token_store_disabled", "reason", "bad_key")
		return nil
	}
	deployment := sha256.Sum256([]byte(strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_PUBLIC_URL")), "/")))
	return &redisstore.TokenStore{Redis: rdb, Sealer: box,
		Prefix: "multica:dws:tokens:" + hex.EncodeToString(deployment[:6]) + ":"}
}
