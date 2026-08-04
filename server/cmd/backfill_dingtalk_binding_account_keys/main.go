package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type options struct {
	Apply bool
}

func parseOptions(args []string) (options, error) {
	var result options
	flags := flag.NewFlagSet("backfill_dingtalk_binding_account_keys", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&result.Apply, "apply", false, "persist verified account keys")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("unexpected positional arguments")
	}
	return result, nil
}

func main() {
	options, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid arguments")
		os.Exit(2)
	}
	if err := run(context.Background(), options, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, options options, output io.Writer) error {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	router, err := agentmessagerouter.NewClient(agentmessagerouter.ClientConfig{
		BaseURL: strings.TrimSpace(os.Getenv("AGENT_MESSAGE_ROUTER_INTERNAL_URL")),
		ServiceCredential: strings.TrimSpace(os.Getenv("AGENT_MESSAGE_ROUTER_SERVICE_CREDENTIAL")),
	})
	if err != nil {
		return errors.New("Agent Message Router configuration is invalid")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return errors.New("database configuration is invalid")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("database is unavailable")
	}
	runner, err := agentmessagerouter.NewAccountKeyBackfillRunner(db.New(pool), router)
	if err != nil {
		return err
	}
	report, err := runner.Run(ctx, options.Apply)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return errors.New("encode account key backfill report")
	}
	return nil
}
