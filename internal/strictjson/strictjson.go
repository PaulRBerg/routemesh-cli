// Package strictjson parses bounded JSON without accepting ambiguous objects.
package strictjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const (
	MaxInputBytes = 1 << 20
	MaxDepth      = 64
)

func Read(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("JSON byte limit must be positive")
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read JSON: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("JSON input exceeds %d bytes", limit)
	}
	return data, nil
}

// Parse returns maps, slices, strings, bools, nil, and json.Number values.
func Parse(data []byte) (any, error) {
	if len(data) > MaxInputBytes {
		return nil, fmt.Errorf("JSON input exceeds %d bytes", MaxInputBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseValue(decoder, 1)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("JSON input contains trailing data")
		}
		return nil, fmt.Errorf("JSON input contains trailing data: %w", err)
	}
	return value, nil
}

func parseValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > MaxDepth {
		return nil, fmt.Errorf("JSON nesting exceeds %d levels", MaxDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return nil, fmt.Errorf("invalid object key: %w", keyErr)
			}
			key, keyOK := keyToken.(string)
			if !keyOK {
				return nil, fmt.Errorf("JSON object key is not a string")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, fmt.Errorf("duplicate JSON object key %q", key)
			}
			value, valueErr := parseValue(decoder, depth+1)
			if valueErr != nil {
				return nil, valueErr
			}
			object[key] = value
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("invalid JSON object")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, valueErr := parseValue(decoder, depth+1)
			if valueErr != nil {
				return nil, valueErr
			}
			array = append(array, value)
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("invalid JSON array")
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}
