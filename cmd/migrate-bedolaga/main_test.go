package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestParseRequiresAMappingAndConfirmation(t *testing.T) {
	for name, args := range map[string][]string{
		"no command":         nil,
		"unknown command":    {"migrate"},
		"plan without map":   {"plan"},
		"apply unconfirmed":  {"apply", "-mapping", "m.yaml"},
		"negative limit":     {"apply", "-mapping", "m.yaml", "-confirm", "-limit", "-1"},
		"stray argument":     {"verify", "-mapping", "m.yaml", "extra"},
		"apply-only flag":    {"plan", "-mapping", "m.yaml", "-confirm"},
		"unknown flag":       {"verify", "-mapping", "m.yaml", "-force"},
		"runs takes no flag": {"runs", "-mapping", "m.yaml"},
	} {
		if _, err := parse(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%s: expected a usage error", name)
		}
	}
}

func TestParseAcceptsEachCommand(t *testing.T) {
	parsed, err := parse([]string{"apply", "-mapping", "m.yaml", "-confirm", "-limit", "100", "-actor", "ops", "-json", "out.json"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if parsed.name != "apply" || parsed.limit != 100 || parsed.actor != "ops" || parsed.jsonPath != "out.json" || !parsed.confirm {
		t.Fatalf("unexpected apply command: %+v", parsed)
	}
	for _, args := range [][]string{{"plan", "-mapping", "m.yaml"}, {"verify", "-mapping", "m.yaml", "-json", "v.json"}, {"runs"}} {
		if _, err := parse(args, &bytes.Buffer{}); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestRunReportsUsageAndConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := run(context.Background(), []string{"apply", "-mapping", "m.yaml"}, &stdout, &stderr); status != exitUsage {
		t.Fatalf("an unconfirmed apply must be a usage error, got %d", status)
	}
	if !strings.Contains(stderr.String(), "-confirm") {
		t.Fatalf("the refusal must say how to proceed: %s", stderr.String())
	}
	t.Setenv("APP_DATABASE_URL", "")
	stderr.Reset()
	if status := run(context.Background(), []string{"plan", "-mapping", "m.yaml"}, &stdout, &stderr); status != exitError {
		t.Fatalf("missing configuration must fail, got %d", status)
	}
	if !strings.Contains(stderr.String(), "APP_DATABASE_URL") {
		t.Fatalf("the error must name the missing variable: %s", stderr.String())
	}
}
