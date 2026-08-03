package evidence

import (
	"context"
	"testing"

	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const transactionHash = "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func transaction() map[string]any {
	return map[string]any{"hash": transactionHash, "blockNumber": "0x2", "blockHash": blockHashA}
}

func receipt() map[string]any {
	return map[string]any{"transactionHash": transactionHash, "blockNumber": "0x2", "blockHash": blockHashA, "transactionIndex": "0x0"}
}

func TestCollectReceiptDirect(t *testing.T) {
	t.Parallel()

	caller := &callerStub{t: t, results: []callResult{
		{method: "eth_getTransactionByHash", value: transaction()},
		{method: "eth_getTransactionReceipt", value: receipt()},
		{method: "eth_getBlockByHash", value: header("0x2", blockHashA)},
	}}
	result, err := CollectReceipt(context.Background(), caller, "1", transactionHash)
	require.NoError(t, err)
	assert.Equal(t, "direct", result.Source)
	assert.Empty(t, caller.results)
}

func TestCollectReceiptRecoversFromExactBlock(t *testing.T) {
	t.Parallel()

	caller := &callerStub{t: t, results: []callResult{
		{method: "eth_getTransactionByHash", value: transaction()},
		{method: "eth_getTransactionReceipt", value: nil},
		{method: "eth_getBlockReceipts", value: []any{receipt()}},
		{method: "eth_getBlockByHash", value: header("0x2", blockHashA)},
	}}
	result, err := CollectReceipt(context.Background(), caller, "1", transactionHash)
	require.NoError(t, err)
	assert.Equal(t, "block_receipts", result.Source)
}

func TestCollectReceiptRejectsContradictoryReceipt(t *testing.T) {
	t.Parallel()

	badReceipt := receipt()
	badReceipt["blockHash"] = blockHashB
	caller := &callerStub{t: t, results: []callResult{
		{method: "eth_getTransactionByHash", value: transaction()},
		{method: "eth_getTransactionReceipt", value: badReceipt},
	}}
	_, err := CollectReceipt(context.Background(), caller, "1", transactionHash)
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Evidence, typed.ExitCode)
}

func TestCollectReceiptRequiresUniqueRecoveryMatch(t *testing.T) {
	t.Parallel()

	caller := &callerStub{t: t, results: []callResult{
		{method: "eth_getTransactionByHash", value: transaction()},
		{method: "eth_getTransactionReceipt", value: nil},
		{method: "eth_getBlockReceipts", value: []any{receipt(), receipt()}},
	}}
	_, err := CollectReceipt(context.Background(), caller, "1", transactionHash)
	require.Error(t, err)
}
