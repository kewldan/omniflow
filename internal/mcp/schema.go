package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	// ErrSchemaUnsupported reports a schema using a construct this validator does
	// not enforce.
	//
	// It is an error at registration time rather than something ignored at
	// validation time. A validator that silently skips a keyword claims to
	// enforce a constraint it does not, which is worse than having no validator:
	// the tool-call preview would show arguments as "validated" when nothing
	// checked them.
	ErrSchemaUnsupported = errors.New("unsupported JSON Schema construct")
	// ErrSchemaInvalid reports a value that does not match its schema.
	ErrSchemaInvalid = errors.New("value does not match its schema")
)

// supported is the keyword set this validator enforces. Anything outside it
// makes the schema unusable rather than partly enforced.
var supported = map[string]bool{
	"type": true, "description": true, "title": true, "enum": true,
	"properties": true, "required": true, "additionalProperties": true,
	"items": true, "minItems": true, "maxItems": true,
	"minLength": true, "maxLength": true, "pattern": true,
	"minimum": true, "maximum": true,
	// Accepted and ignored: they carry no constraint, and refusing a schema for
	// declaring its dialect would reject most real servers.
	"$schema": true, "default": true, "examples": true,
}

// Schema is the subset of JSON Schema Omniflow enforces on MCP tool arguments
// and results.
//
// It is a subset on purpose. Full JSON Schema includes composition keywords
// (`anyOf`, `$ref`, `if`/`then`) whose interaction with a security boundary is
// subtle, and a tool whose arguments cannot be described without them is a tool
// an owner should look at before connecting.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Description          string             `json:"description,omitempty"`
	Enum                 []any              `json:"enum,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	MinItems             *int               `json:"minItems,omitempty"`
	MaxItems             *int               `json:"maxItems,omitempty"`
	MinLength            *int               `json:"minLength,omitempty"`
	MaxLength            *int               `json:"maxLength,omitempty"`
	Pattern              string             `json:"pattern,omitempty"`
	Minimum              *float64           `json:"minimum,omitempty"`
	Maximum              *float64           `json:"maximum,omitempty"`

	pattern *regexp.Regexp
}

// CompileSchema reads a schema document and refuses one it cannot enforce.
func CompileSchema(raw []byte) (*Schema, error) {
	if len(raw) == 0 {
		// A tool with no declared input schema takes no arguments. That is a
		// closed schema, not an absent one — otherwise "no schema" would be the
		// way to send anything.
		return &Schema{Type: "object", AdditionalProperties: &closed}, nil
	}
	return compile(raw, "")
}

func compile(raw []byte, path string) (*Schema, error) {
	var keywords map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keywords); err != nil {
		return nil, fmt.Errorf("%w: %s is not a schema object", ErrSchemaUnsupported, label(path))
	}
	unknown := make([]string, 0, 2)
	for keyword := range keywords {
		if !supported[keyword] {
			unknown = append(unknown, keyword)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("%w: %s uses %s",
			ErrSchemaUnsupported, label(path), strings.Join(unknown, ", "))
	}

	var schema Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("%w: %s is malformed", ErrSchemaUnsupported, label(path))
	}
	switch schema.Type {
	case "", "object", "array", "string", "number", "integer", "boolean", "null":
	default:
		return nil, fmt.Errorf("%w: %s declares type %q",
			ErrSchemaUnsupported, label(path), schema.Type)
	}
	if schema.Pattern != "" {
		compiled, err := regexp.Compile(schema.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: %s has an uncompilable pattern",
				ErrSchemaUnsupported, label(path))
		}
		schema.pattern = compiled
	}

	// Children are compiled from their raw text so an unsupported keyword
	// nested three levels down is still caught at registration.
	if properties, present := keywords["properties"]; present {
		var children map[string]json.RawMessage
		if err := json.Unmarshal(properties, &children); err != nil {
			return nil, fmt.Errorf("%w: %s has malformed properties",
				ErrSchemaUnsupported, label(path))
		}
		schema.Properties = make(map[string]*Schema, len(children))
		for name, child := range children {
			compiled, err := compile(child, join(path, name))
			if err != nil {
				return nil, err
			}
			schema.Properties[name] = compiled
		}
	}
	if items, present := keywords["items"]; present {
		compiled, err := compile(items, join(path, "[]"))
		if err != nil {
			return nil, err
		}
		schema.Items = compiled
	}
	return &schema, nil
}

// Validate checks a decoded JSON value and returns every problem it found.
//
// Every problem rather than the first, because an operator fixing a tool call
// one rejection at a time learns nothing about the shape it wanted.
func (schema *Schema) Validate(value any) []string {
	if schema == nil {
		return nil
	}
	problems := make([]string, 0, 4)
	schema.check(value, "", &problems)
	sort.Strings(problems)
	return problems
}

func (schema *Schema) check(value any, path string, problems *[]string) {
	if value == nil {
		if schema.Type != "" && schema.Type != "null" {
			*problems = append(*problems, label(path)+" is missing")
		}
		return
	}

	switch schema.Type {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			*problems = append(*problems, label(path)+" must be an object")
			return
		}
		for _, name := range schema.Required {
			if _, present := object[name]; !present {
				*problems = append(*problems, label(join(path, name))+" is required")
			}
		}
		// The default is closed. An MCP server that gains a field between a
		// capability refresh and a call should not have that field forwarded
		// unexamined, and a client that forwards unknown arguments is how a
		// tool-confusion attack adds one.
		open := schema.AdditionalProperties != nil && *schema.AdditionalProperties
		for name, child := range object {
			property, declared := schema.Properties[name]
			if !declared {
				if !open {
					*problems = append(*problems, label(join(path, name))+" is not a declared argument")
				}
				continue
			}
			property.check(child, join(path, name), problems)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			*problems = append(*problems, label(path)+" must be an array")
			return
		}
		if schema.MinItems != nil && len(items) < *schema.MinItems {
			*problems = append(*problems, fmt.Sprintf("%s needs at least %d items",
				label(path), *schema.MinItems))
		}
		if schema.MaxItems != nil && len(items) > *schema.MaxItems {
			*problems = append(*problems, fmt.Sprintf("%s allows at most %d items",
				label(path), *schema.MaxItems))
		}
		if schema.Items != nil {
			for index, item := range items {
				schema.Items.check(item, fmt.Sprintf("%s[%d]", path, index), problems)
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			*problems = append(*problems, label(path)+" must be text")
			return
		}
		if schema.MinLength != nil && len([]rune(text)) < *schema.MinLength {
			*problems = append(*problems, fmt.Sprintf("%s needs at least %d characters",
				label(path), *schema.MinLength))
		}
		if schema.MaxLength != nil && len([]rune(text)) > *schema.MaxLength {
			*problems = append(*problems, fmt.Sprintf("%s allows at most %d characters",
				label(path), *schema.MaxLength))
		}
		if schema.pattern != nil && !schema.pattern.MatchString(text) {
			*problems = append(*problems, label(path)+" does not match the required format")
		}
	case "number", "integer":
		number, ok := value.(float64)
		if !ok {
			*problems = append(*problems, label(path)+" must be a number")
			return
		}
		if schema.Type == "integer" && number != float64(int64(number)) {
			*problems = append(*problems, label(path)+" must be a whole number")
		}
		if schema.Minimum != nil && number < *schema.Minimum {
			*problems = append(*problems, fmt.Sprintf("%s must be at least %v",
				label(path), *schema.Minimum))
		}
		if schema.Maximum != nil && number > *schema.Maximum {
			*problems = append(*problems, fmt.Sprintf("%s must be at most %v",
				label(path), *schema.Maximum))
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			*problems = append(*problems, label(path)+" must be true or false")
		}
	case "null":
		*problems = append(*problems, label(path)+" must be empty")
	}

	if len(schema.Enum) > 0 && !inEnum(value, schema.Enum) {
		*problems = append(*problems, label(path)+" is not one of the allowed values")
	}
}

func inEnum(value any, allowed []any) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	for _, candidate := range allowed {
		other, err := json.Marshal(candidate)
		if err == nil && string(other) == string(encoded) {
			return true
		}
	}
	return false
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func label(path string) string {
	if path == "" {
		return "the value"
	}
	return path
}

// closed is the additionalProperties value an undeclared schema gets. It is a
// variable only because JSON Schema distinguishes "absent" from "false", and
// that distinction is the difference between an open object and a closed one.
var closed = false
