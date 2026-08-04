package main

import "testing"

func TestParseOptionsDefaultsToDryRun(t *testing.T) {
	options, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.Apply {
		t.Fatal("backfill must default to dry-run")
	}
}

func TestParseOptionsRequiresExplicitApply(t *testing.T) {
	options, err := parseOptions([]string{"--apply"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.Apply {
		t.Fatal("--apply was ignored")
	}
}
