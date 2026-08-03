package jsonrpc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRawNormalizesAndPreservesIDs(t *testing.T) {
	t.Parallel()

	envelope, err := ParseRaw([]byte(`[
        {"jsonrpc":"2.0","method":"eth_chainId","id":"agent"},
        {"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":2}
    ]`))
	require.NoError(t, err)
	assert.True(t, envelope.Batch)
	assert.Equal(t, "agent", envelope.Requests[0].ID)
	assert.Equal(t, []any{}, envelope.Requests[0].Params)
}

func TestParseRawRejectsMalformedEnvelopes(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"empty batch":       `[]`,
		"unknown field":     `{"jsonrpc":"2.0","method":"eth_chainId","id":1,"extra":true}`,
		"bad version":       `{"jsonrpc":"1.0","method":"eth_chainId","id":1}`,
		"notification":      `{"jsonrpc":"2.0","method":"eth_chainId"}`,
		"fractional id":     `{"jsonrpc":"2.0","method":"eth_chainId","id":1.5}`,
		"duplicate ids":     `[{"jsonrpc":"2.0","method":"a","id":1},{"jsonrpc":"2.0","method":"b","id":1}]`,
		"forbidden method":  `{"jsonrpc":"2.0","method":"../../.ssh","id":1}`,
		"encoded traversal": `{"jsonrpc":"2.0","method":"%2e%2e","id":1}`,
		"URL fragment":      `{"jsonrpc":"2.0","method":"eth_call#x","id":1}`,
		"scalar params":     `{"jsonrpc":"2.0","method":"eth_call","params":"x","id":1}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseRaw([]byte(input))
			require.Error(t, err)
		})
	}
}

func TestBatchLimit(t *testing.T) {
	t.Parallel()

	item := `{"jsonrpc":"2.0","method":"eth_chainId","id":"x"}`
	input := "[" + strings.Repeat(item+",", MaxBatchRequests) + item + "]"
	_, err := ParseRaw([]byte(input))
	require.ErrorContains(t, err, "1 to 100")
}

func TestWriteClassification(t *testing.T) {
	t.Parallel()

	for _, method := range []string{"eth_sendRawTransaction", "eth_sendUserOperation", "eth_submitWork", "personal_sign", "engine_newPayloadV3", "anvil_setBalance", "eth_newFilter"} {
		assert.True(t, IsWriteMethod(method), method)
	}
	for _, method := range []string{"eth_call", "eth_getLogs", "eth_getFilterChanges", "debug_traceTransaction"} {
		assert.False(t, IsWriteMethod(method), method)
	}
}

func TestOpaqueParamsPermitApplicationControlCharacters(t *testing.T) {
	t.Parallel()

	envelope, err := ParseRaw([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"eth_call\",\"params\":[\"line\\n\\u001b\"],\"id\":1}"))
	require.NoError(t, err)
	params := envelope.Requests[0].Params.([]any)
	assert.Equal(t, "line\n\x1b", params[0])
}

func FuzzParseRaw(f *testing.F) {
	for _, input := range []string{
		`{"jsonrpc":"2.0","method":"eth_chainId","id":1}`,
		`{"jsonrpc":"2.0","method":"../../.ssh","id":1}`,
		`{"jsonrpc":"2.0","method":"eth_call?x","id":1}`,
		"{\"jsonrpc\":\"2.0\",\"method\":\"eth_call\\u0000\",\"id\":1}",
	} {
		f.Add(input)
	}
	f.Fuzz(func(_ *testing.T, input string) {
		_, _ = ParseRaw([]byte(input))
	})
}
