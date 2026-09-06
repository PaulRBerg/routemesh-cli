package evidence

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionInputValidation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		kind   string
		filter string
		valid  bool
	}{
		{"newHeads", "", true},
		{"newPendingTransactions", "", true},
		{"logs", "", true},
		{"logs", `{"address":"` + address + `","topics":[null,["` + topic + `",null]]}`, true},
		{"logs", `{"address":["` + address + `"],"topics":[]}`, true},
		{"unknown", "", false},
		{"newHeads", `{}`, false},
		{"newPendingTransactions", `{}`, false},
		{"logs", `[]`, false},
		{"logs", `{"fromBlock":"latest"}`, false},
		{"logs", `{"blockHash":"` + blockHashA + `"}`, false},
		{"logs", `{"address":[]}`, false},
		{"logs", `{"address":"0x1234"}`, false},
		{"logs", `{"topics":[[]]}`, false},
		{"logs", `{"topics":[null,null,null,null,null]}`, false},
		{"logs", `{"topics":[],"topics":[]}`, false},
	} {
		t.Run(test.kind+test.filter, func(t *testing.T) {
			t.Parallel()
			subscription, err := ParseSubscription(test.kind, []byte(test.filter))
			require.Equal(t, test.valid, err == nil)
			if !test.valid {
				return
			}
			request, err := subscription.Request()
			require.NoError(t, err)
			assert.Equal(t, "eth_subscribe", request.Requests[0].Method)
			assert.Equal(t, test.kind, request.Requests[0].Params.([]any)[0])
		})
	}
}

func TestSubscriptionHeadsAndPendingHashes(t *testing.T) {
	t.Parallel()

	heads, err := ParseSubscription("newHeads", nil)
	require.NoError(t, err)
	require.NoError(t, heads.ValidateResult(map[string]any{
		"number": "0x1", "hash": blockHashA, "parentHash": blockHashB, "extraField": "allowed",
	}))
	for _, value := range []any{
		nil,
		map[string]any{"hash": blockHashA, "parentHash": blockHashB},
		map[string]any{"number": "0x01", "hash": blockHashA, "parentHash": blockHashB},
		map[string]any{"number": "0x1", "hash": "0x0", "parentHash": blockHashB},
		map[string]any{"number": "0x1", "hash": blockHashA},
	} {
		require.Error(t, heads.ValidateResult(value))
	}
	pending, err := ParseSubscription("newPendingTransactions", nil)
	require.NoError(t, err)
	require.NoError(t, pending.ValidateResult(transactionHash))
	require.Error(t, pending.ValidateResult("0x1"))
	require.Error(t, pending.ValidateResult(map[string]any{"hash": transactionHash}))
}

func subscriptionLog() map[string]any {
	return map[string]any{
		"address": address, "topics": []any{topic}, "data": "0x1234",
		"blockNumber": "0x1", "transactionHash": transactionHash, "transactionIndex": "0x0",
		"blockHash": blockHashA, "logIndex": "0x0", "removed": false,
	}
}

func TestSubscriptionLogsPreserveReorgRemovalsAndValidateFilters(t *testing.T) {
	t.Parallel()

	subscription, err := ParseSubscription("logs", []byte(`{"address":["`+address+`"],"topics":[["`+topic+`"]]}`))
	require.NoError(t, err)
	log := subscriptionLog()
	require.NoError(t, subscription.ValidateResult(log))
	log["removed"] = true
	require.NoError(t, subscription.ValidateResult(log))
	_, _, err = validateLog(log, Chunk{From: 1, To: 1})
	require.Error(t, err, "historical log evidence must still reject removed logs")

	log["address"] = "0x" + strings.Repeat("b", 40)
	require.Error(t, subscription.ValidateResult(log))
	log = subscriptionLog()
	log["topics"] = []any{blockHashB}
	require.Error(t, subscription.ValidateResult(log))
	log["topics"] = []any{}
	require.Error(t, subscription.ValidateResult(log))
	log = subscriptionLog()
	delete(log, "removed")
	require.Error(t, subscription.ValidateResult(log))
	log = subscriptionLog()
	log["transactionIndex"] = "0x00"
	require.Error(t, subscription.ValidateResult(log))

	wildcard, err := ParseSubscription("logs", []byte(`{"topics":[[null]]}`))
	require.NoError(t, err)
	require.NoError(t, wildcard.ValidateResult(subscriptionLog()))
}
