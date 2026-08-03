package evidence

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/jsonrpc"
	"github.com/paulrberg/routemesh-cli/internal/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	blockHashA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	blockHashB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type callResult struct {
	method string
	value  any
}

type callerStub struct {
	t       *testing.T
	results []callResult
	calls   []jsonrpc.Envelope
}

func (c *callerStub) DoRPC(_ context.Context, _ string, envelope jsonrpc.Envelope) (transport.RPCResult, error) {
	c.t.Helper()
	require.NotEmpty(c.t, c.results)
	expected := c.results[0]
	c.results = c.results[1:]
	require.Equal(c.t, expected.method, envelope.Requests[0].Method)
	c.calls = append(c.calls, envelope)
	return transport.RPCResult{Value: map[string]any{"jsonrpc": "2.0", "id": json.Number("1"), "result": expected.value}}, nil
}

func header(number, hash string) map[string]any {
	return map[string]any{"number": number, "hash": hash}
}

func TestCollectLogsResolvesLatestOnceAndEmitsCanonicalEmptyEvidence(t *testing.T) {
	t.Parallel()

	filter, err := ParseLogFilter([]byte(`{"fromBlock":"0x1","toBlock":"latest"}`))
	require.NoError(t, err)
	caller := &callerStub{t: t, results: []callResult{
		{method: "eth_blockNumber", value: "0x2"},
		{method: "eth_getBlockByNumber", value: header("0x2", blockHashA)},
		{method: "eth_getLogs", value: []any{}},
		{method: "eth_getBlockByNumber", value: header("0x2", blockHashA)},
	}}
	result, err := CollectLogs(context.Background(), caller, "1", filter)
	require.NoError(t, err)
	assert.Empty(t, result.Logs)
	assert.Equal(t, 1, result.ChunkCount)
	assert.Len(t, result.Records(), 3)
	assert.Empty(t, caller.results)
}

func TestCollectLogsRejectsUpperBoundReorg(t *testing.T) {
	t.Parallel()

	filter, err := ParseLogFilter([]byte(`{"fromBlock":"0x1","toBlock":"0x1"}`))
	require.NoError(t, err)
	caller := &callerStub{t: t, results: []callResult{
		{method: "eth_getBlockByNumber", value: header("0x1", blockHashA)},
		{method: "eth_getLogs", value: []any{}},
		{method: "eth_getBlockByNumber", value: header("0x1", blockHashB)},
	}}
	_, err = CollectLogs(context.Background(), caller, "1", filter)
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Evidence, typed.ExitCode)
	assert.Equal(t, "reorg_detected", typed.Kind)
}
