// Package output buffers, selects, bounds, and writes CLI JSON output.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const DefaultMaxBytes int64 = 1 << 20

type Config struct {
	Format   string
	Pretty   bool
	Pointers []string
	MaxBytes int64
}

type Document struct {
	JSON   any
	NDJSON []any
}

type selection struct {
	Pointer string `json:"pointer"`
	Value   any    `json:"value"`
}

func Write(writer io.Writer, config Config, document Document) error {
	encoded, err := Encode(config, document)
	if err != nil {
		return err
	}
	if _, err := writer.Write(encoded); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func Encode(config Config, document Document) ([]byte, error) {
	if err := Validate(config); err != nil {
		return nil, err
	}

	var buffer bytes.Buffer
	if config.Format == "json" {
		value, err := applyPointers(document.JSON, config.Pointers)
		if err != nil {
			return nil, err
		}
		var encoded []byte
		if config.Pretty {
			encoded, err = json.MarshalIndent(value, "", "  ")
		} else {
			encoded, err = json.Marshal(value)
		}
		if err != nil {
			return nil, fmt.Errorf("encode JSON output: %w", err)
		}
		buffer.Write(encoded)
		buffer.WriteByte('\n')
	} else {
		records := document.NDJSON
		if records == nil {
			records = []any{document.JSON}
		}
		selected := make([]any, len(records))
		for i, record := range records {
			value, err := applyPointers(record, config.Pointers)
			if err != nil {
				return nil, fmt.Errorf("NDJSON record %d: %w", i, err)
			}
			selected[i] = value
		}
		for _, record := range selected {
			encoded, err := json.Marshal(record)
			if err != nil {
				return nil, fmt.Errorf("encode NDJSON output: %w", err)
			}
			buffer.Write(encoded)
			buffer.WriteByte('\n')
		}
	}
	if int64(buffer.Len()) > config.MaxBytes {
		return nil, fmt.Errorf("encoded output is %d bytes, exceeding limit %d", buffer.Len(), config.MaxBytes)
	}
	return buffer.Bytes(), nil
}

func Validate(config Config) error {
	if config.MaxBytes <= 0 {
		return fmt.Errorf("max output bytes must be positive")
	}
	if config.Format != "json" && config.Format != "ndjson" {
		return fmt.Errorf("output must be json or ndjson")
	}
	if config.Pretty && config.Format == "ndjson" {
		return fmt.Errorf("--pretty cannot be used with NDJSON output")
	}
	for _, pointer := range config.Pointers {
		if pointer == "" {
			continue
		}
		if !strings.HasPrefix(pointer, "/") {
			return fmt.Errorf("JSON Pointer %q must be empty or start with /", pointer)
		}
		for _, token := range strings.Split(pointer[1:], "/") {
			if _, err := decodeToken(token); err != nil {
				return fmt.Errorf("invalid JSON Pointer %q: %w", pointer, err)
			}
		}
	}
	return nil
}

func applyPointers(value any, pointers []string) (any, error) {
	if len(pointers) == 0 {
		return value, nil
	}
	selected := make([]selection, len(pointers))
	for i, pointer := range pointers {
		item, err := Resolve(value, pointer)
		if err != nil {
			return nil, err
		}
		selected[i] = selection{Pointer: pointer, Value: item}
	}
	if len(selected) == 1 {
		return selected[0].Value, nil
	}
	return selected, nil
}

// Resolve evaluates an RFC 6901 JSON Pointer against decoded JSON.
func Resolve(value any, pointer string) (any, error) {
	if pointer == "" {
		return value, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("JSON Pointer %q must be empty or start with /", pointer)
	}
	current := value
	for _, rawToken := range strings.Split(pointer[1:], "/") {
		token, err := decodeToken(rawToken)
		if err != nil {
			return nil, fmt.Errorf("invalid JSON Pointer %q: %w", pointer, err)
		}
		switch typed := current.(type) {
		case map[string]any:
			next, exists := typed[token]
			if !exists {
				return nil, fmt.Errorf("JSON Pointer %q is missing", pointer)
			}
			current = next
		case []any:
			if token == "" || (len(token) > 1 && token[0] == '0') {
				return nil, fmt.Errorf("JSON Pointer %q has an invalid array index", pointer)
			}
			index, parseErr := strconv.Atoi(token)
			if parseErr != nil || index < 0 || index >= len(typed) {
				return nil, fmt.Errorf("JSON Pointer %q is missing", pointer)
			}
			current = typed[index]
		default:
			return nil, fmt.Errorf("JSON Pointer %q traverses a scalar", pointer)
		}
	}
	return current, nil
}

func decodeToken(token string) (string, error) {
	var builder strings.Builder
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			builder.WriteByte(token[i])
			continue
		}
		if i+1 >= len(token) {
			return "", fmt.Errorf("dangling ~ escape")
		}
		i++
		switch token[i] {
		case '0':
			builder.WriteByte('~')
		case '1':
			builder.WriteByte('/')
		default:
			return "", fmt.Errorf("unknown ~%c escape", token[i])
		}
	}
	return builder.String(), nil
}
