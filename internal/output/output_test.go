package output

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeCompactJSONAndSelection(t *testing.T) {
	t.Parallel()

	document := Document{JSON: map[string]any{"a/b": map[string]any{"~key": "value"}, "other": true}}
	encoded, err := Encode(Config{Format: "json", Pointers: []string{"/a~1b/~0key"}, MaxBytes: 1024}, document)
	require.NoError(t, err)
	assert.Equal(t, `"value"`+"\n", string(encoded))
}

func TestEncodeRepeatedSelectionsAreSelfDescribing(t *testing.T) {
	t.Parallel()

	document := Document{JSON: map[string]any{"a": 1, "b": 2}}
	encoded, err := Encode(Config{Format: "json", Pointers: []string{"/a", "/b"}, MaxBytes: 1024}, document)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"pointer":"/a","value":1},{"pointer":"/b","value":2}]`, string(encoded))
}

func TestEncodeMissingPointerWritesNothing(t *testing.T) {
	t.Parallel()

	var writer strings.Builder
	err := Write(&writer, Config{Format: "json", Pointers: []string{"/missing"}, MaxBytes: 1024}, Document{JSON: map[string]any{}})
	require.ErrorContains(t, err, "missing")
	assert.Empty(t, writer.String())
}

func TestEncodeNDJSONAndOutputLimit(t *testing.T) {
	t.Parallel()

	records := []any{map[string]any{"id": 1}, map[string]any{"id": 2}}
	encoded, err := Encode(Config{Format: "ndjson", MaxBytes: 1024}, Document{NDJSON: records})
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(encoded)), "\n")
	require.Len(t, lines, 2)
	for _, line := range lines {
		var value any
		require.NoError(t, json.Unmarshal([]byte(line), &value))
	}

	_, err = Encode(Config{Format: "ndjson", MaxBytes: 8}, Document{NDJSON: records})
	require.ErrorContains(t, err, "exceeding limit")
}

func TestEncodeEscapesTerminalControlsAndRepairsUTF8(t *testing.T) {
	t.Parallel()

	value := string([]byte{'a', 0x1b, '\n', 0xff})
	encoded, err := Encode(Config{Format: "json", MaxBytes: 1024}, Document{JSON: value})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), string(rune(0x1b)))
	assert.NotContains(t, string(encoded), "\x1b")
	assert.Contains(t, string(encoded), `\u001b`)
	assert.True(t, json.Valid(encoded))
}
