package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func decode(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("fixture is not json: %v", err)
	}
	return value
}

// A validator that skips a keyword claims to enforce a constraint it does not,
// which is worse than having no validator at all.
func TestAnUnenforceableSchemaIsRefusedAtCompileTime(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","anyOf":[{"type":"string"}]}`,
		`{"type":"object","properties":{"id":{"$ref":"#/definitions/id"}}}`,
		`{"type":"object","properties":{"nested":{"type":"object","properties":{"x":{"oneOf":[]}}}}}`,
		`{"type":"tuple"}`,
	} {
		if _, err := CompileSchema([]byte(schema)); !errors.Is(err, ErrSchemaUnsupported) {
			t.Fatalf("%s was accepted: %v", schema, err)
		}
	}
}

// An absent schema means a tool takes no arguments. If it meant "anything
// goes", omitting a schema would be the way to send anything.
func TestAnAbsentSchemaIsClosedRatherThanOpen(t *testing.T) {
	schema, err := CompileSchema(nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	problems := schema.Validate(decode(t, `{"anything":"at all"}`))
	if len(problems) == 0 {
		t.Fatal("an undeclared argument passed a schema with no properties")
	}
}

// A client that forwards unknown arguments is how a tool-confusion attack adds
// one, so objects are closed unless a server says otherwise.
func TestUndeclaredArgumentsAreRefusedByDefault(t *testing.T) {
	schema, err := CompileSchema([]byte(
		`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	problems := schema.Validate(decode(t, `{"query":"orders","__proto__":"x","admin":true}`))
	if len(problems) != 2 {
		t.Fatalf("expected both undeclared arguments to be caught, got %v", problems)
	}

	open, err := CompileSchema([]byte(
		`{"type":"object","properties":{"query":{"type":"string"}},"additionalProperties":true}`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if problems := open.Validate(decode(t, `{"query":"x","extra":1}`)); len(problems) != 0 {
		t.Fatalf("an explicitly open schema rejected an extra field: %v", problems)
	}
}

// Every problem rather than the first: an operator fixing a call one rejection
// at a time learns nothing about the shape it wanted.
func TestValidationReportsEveryProblem(t *testing.T) {
	schema, err := CompileSchema([]byte(`{
		"type":"object",
		"properties":{
			"status":{"type":"string","enum":["open","closed"]},
			"limit":{"type":"integer","minimum":1,"maximum":50},
			"code":{"type":"string","pattern":"^[A-Z]{3}$"},
			"tags":{"type":"array","items":{"type":"string"},"maxItems":2}
		},
		"required":["status","limit"]
	}`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	problems := schema.Validate(decode(t,
		`{"status":"pending","limit":900,"code":"abcd","tags":["a","b","c"]}`))
	if len(problems) != 4 {
		t.Fatalf("expected the enum, maximum, pattern, and maxItems problems, got %v", problems)
	}

	missing := schema.Validate(decode(t, `{}`))
	if len(missing) != 2 {
		t.Fatalf("expected both required fields to be reported, got %v", missing)
	}
	for _, problem := range missing {
		if !strings.Contains(problem, "is required") {
			t.Fatalf("a missing field was not reported as required: %q", problem)
		}
	}
}

// An integer field that accepts 1.5 is a field that reaches the far side as
// something other than what the contract promised.
func TestIntegerFieldsRejectFractions(t *testing.T) {
	schema, err := CompileSchema([]byte(`{"type":"object","properties":{"n":{"type":"integer"}}}`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if problems := schema.Validate(decode(t, `{"n":1.5}`)); len(problems) != 1 {
		t.Fatalf("a fractional integer was accepted: %v", problems)
	}
	if problems := schema.Validate(decode(t, `{"n":42}`)); len(problems) != 0 {
		t.Fatalf("a whole number was rejected: %v", problems)
	}
}
