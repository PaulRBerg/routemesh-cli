package schema

import (
	"bytes"
	"encoding/json"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compileDetail(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	detail, err := Detail(name)
	require.NoError(t, err)
	data, err := json.Marshal(detail)
	require.NoError(t, err)
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	require.NoError(t, err)
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("https://example.test/"+name, document))
	compiled, err := compiler.Compile("https://example.test/" + name)
	require.NoError(t, err)
	return compiled
}

func TestAllCommandSchemasCompile(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"schema", "init", "auth-status", "auth-clear", "health", "chains", "ping", "rpc", "logs", "receipt"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_ = compileDetail(t, name)
		})
	}
}

func TestRepresentativeOutputsMatchSchemas(t *testing.T) {
	t.Parallel()

	health := compileDetail(t, "health")
	require.NoError(t, health.Validate(map[string]any{"ready": true, "message": "ready", "latency_ms": 12}))
	require.Error(t, health.Validate(map[string]any{"ready": true, "message": "ready"}))

	chains := compileDetail(t, "chains")
	require.NoError(t, chains.Validate([]any{map[string]any{"chain_id": "1", "name": "Ethereum"}}))
	require.NoError(t, chains.Validate([]any{map[string]any{"chain_id": "18446744073709551615", "name": "Maximum"}}))
	require.Error(t, chains.Validate([]any{map[string]any{"chain_id": "01", "name": "Ethereum"}}))
	require.Error(t, chains.Validate([]any{map[string]any{"chain_id": "18446744073709551616", "name": "Overflow"}}))

	rpc := compileDetail(t, "rpc")
	require.NoError(t, rpc.Validate(map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0x1"}))
}

func TestValidateDefinition(t *testing.T) {
	t.Parallel()

	valid := map[string]any{"ready": true, "message": "ready", "latency_ms": 1}
	require.NoError(t, ValidateDefinition("health", "output", valid))
	require.Error(t, ValidateDefinition("health", "output", map[string]any{"ready": true}))
	require.Error(t, ValidateDefinition("health", "unknown", valid))
	require.NoError(t, ValidateDefinition("ping", "output", map[string]any{
		"chain_id": "1", "block_number": "1", "block_number_hex": "0x1", "latency_ms": int64(1),
		"routes": []string{"eth_chainId", "eth_blockNumber"},
	}))
}

func TestSchemasMarkProviderContentUntrusted(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"health", "chains", "rpc", "logs", "receipt"} {
		detail, err := Detail(name)
		require.NoError(t, err)
		assert.Truef(t, containsUntrusted(detail), "%s schema lacks an untrusted annotation", name)
	}
}

func TestIndexDescribesEveryDetail(t *testing.T) {
	t.Parallel()

	index, err := Index()
	require.NoError(t, err)
	entries, ok := index["commands"].([]IndexEntry)
	require.True(t, ok)
	assert.Len(t, entries, 11)
	for _, entry := range entries {
		assert.NotEmpty(t, entry.Summary)
		assert.Contains(t, []string{"read_only", "external_write", "conditional"}, entry.SideEffect)
		assert.Equal(t, []string{"json", "ndjson"}, entry.OutputFormats)
	}
}

func containsUntrusted(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if marked, ok := typed["x-routemesh-untrusted"].(bool); ok && marked {
			return true
		}
		for _, child := range typed {
			if containsUntrusted(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsUntrusted(child) {
				return true
			}
		}
	}
	return false
}
