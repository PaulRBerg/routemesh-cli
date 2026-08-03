// Package schema exposes the CLI's embedded runtime contract catalog.
package schema

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed catalog.json
var catalogJSON []byte

type command struct {
	Summary    string         `json:"summary"`
	SideEffect string         `json:"side_effect"`
	InputModes []string       `json:"input_modes"`
	Input      map[string]any `json:"input"`
	Output     map[string]any `json:"output"`
	DryRun     map[string]any `json:"dry_run"`
}

type catalog struct {
	Common   map[string]map[string]any `json:"common"`
	Commands map[string]command        `json:"commands"`
}

type IndexEntry struct {
	Name          string   `json:"name"`
	Summary       string   `json:"summary"`
	SideEffect    string   `json:"side_effect"`
	InputModes    []string `json:"input_modes"`
	OutputFormats []string `json:"output_formats"`
	SchemaCommand string   `json:"schema_command"`
}

func load() (catalog, error) {
	var parsed catalog
	if err := json.Unmarshal(catalogJSON, &parsed); err != nil {
		return catalog{}, fmt.Errorf("decode embedded schema catalog: %w", err)
	}
	return parsed, nil
}

func Index() (map[string]any, error) {
	parsed, err := load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(parsed.Commands))
	for name := range parsed.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]IndexEntry, 0, len(names)+1)
	for _, name := range names {
		item := parsed.Commands[name]
		displayName := strings.ReplaceAll(name, "-", " ")
		entries = append(entries, IndexEntry{
			Name:          displayName,
			Summary:       item.Summary,
			SideEffect:    item.SideEffect,
			InputModes:    item.InputModes,
			OutputFormats: []string{"json", "ndjson"},
			SchemaCommand: "routemesh schema " + displayName,
		})
	}
	entries = append(entries, IndexEntry{
		Name:          "schema api",
		Summary:       "Fetch RouteMesh's current official OpenAPI document.",
		SideEffect:    "read_only",
		InputModes:    []string{"none"},
		OutputFormats: []string{"json", "ndjson"},
		SchemaCommand: "routemesh schema schema",
	})
	return map[string]any{
		"schema_draft": "https://json-schema.org/draft/2020-12/schema",
		"commands":     entries,
	}, nil
}

func Detail(parts ...string) (map[string]any, error) {
	parsed, err := load()
	if err != nil {
		return nil, err
	}
	name := strings.ReplaceAll(strings.Join(parts, "-"), " ", "-")
	item, exists := parsed.Commands[name]
	if !exists {
		return nil, fmt.Errorf("unknown command %q", strings.Join(parts, " "))
	}
	defs := make(map[string]any, len(parsed.Common)+5)
	for key, value := range parsed.Common {
		defs[key] = value
	}
	defs["input"] = item.Input
	defs["output"] = item.Output
	defs["stderr_event"] = parsed.Common["stderr_event"]
	defs["dry_run"] = item.DryRun
	defs["exit_codes"] = parsed.Common["exit_codes"]
	if name == "rpc" {
		defs["jsonrpc_request"] = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"jsonrpc": map[string]any{"const": "2.0"},
				"method":  map[string]any{"type": "string", "pattern": `^[A-Za-z0-9_.:-]{1,128}$`},
				"params":  map[string]any{"oneOf": []any{map[string]any{"type": "array"}, map[string]any{"type": "object"}}, "description": "Method-dependent opaque JSON.", "x-routemesh-untrusted": true},
				"id":      map[string]any{"type": []string{"string", "integer"}},
			},
			"required":             []string{"jsonrpc", "method", "id"},
			"additionalProperties": false,
		}
	}
	return map[string]any{
		"$schema":                      "https://json-schema.org/draft/2020-12/schema",
		"$id":                          "https://github.com/PaulRBerg/routemesh-cli/schema/" + name,
		"title":                        "routemesh " + strings.ReplaceAll(name, "-", " ") + " contract",
		"description":                  item.Summary,
		"anyOf":                        []any{map[string]any{"$ref": "#/$defs/output"}, map[string]any{"$ref": "#/$defs/dry_run"}},
		"$defs":                        defs,
		"x-routemesh-command":          strings.ReplaceAll(name, "-", " "),
		"x-routemesh-side-effect":      item.SideEffect,
		"x-routemesh-input-modes":      item.InputModes,
		"x-routemesh-output-formats":   []string{"json", "ndjson"},
		"x-routemesh-input-schema":     "#/$defs/input",
		"x-routemesh-output-schema":    "#/$defs/output",
		"x-routemesh-stderr-schema":    "#/$defs/stderr_event",
		"x-routemesh-dry-run-schema":   "#/$defs/dry_run",
		"x-routemesh-exit-code-schema": "#/$defs/exit_codes",
	}, nil
}

func ValidateDefinition(commandName, definition string, value any) error {
	detail, err := Detail(strings.Split(commandName, "-")...)
	if err != nil {
		return err
	}
	if definition != "input" && definition != "output" && definition != "stderr_event" && definition != "dry_run" && definition != "exit_codes" {
		return fmt.Errorf("unknown schema definition %q", definition)
	}
	delete(detail, "anyOf")
	detail["$ref"] = "#/$defs/" + definition
	encoded, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode %s schema: %w", commandName, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("parse %s schema: %w", commandName, err)
	}
	resource := "https://routemesh-cli.invalid/schema/" + commandName + "/" + definition
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(resource, document); err != nil {
		return fmt.Errorf("load %s schema: %w", commandName, err)
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return fmt.Errorf("compile %s schema: %w", commandName, err)
	}
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s %s value: %w", commandName, definition, err)
	}
	var normalized any
	if err := json.Unmarshal(valueJSON, &normalized); err != nil {
		return fmt.Errorf("normalize %s %s value: %w", commandName, definition, err)
	}
	if err := compiled.Validate(normalized); err != nil {
		return fmt.Errorf("%s %s does not match its bundled schema: %w", commandName, definition, err)
	}
	return nil
}

func EmbeddedCatalog() []byte {
	return append([]byte(nil), catalogJSON...)
}
