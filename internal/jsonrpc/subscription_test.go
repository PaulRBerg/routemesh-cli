package jsonrpc

import (
	"testing"

	"github.com/paulrberg/routemesh-cli/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionNotificationEnvelope(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"valid", `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"sub-1","result":{"number":"0x1"}}}`, true},
		{"null result remains method dependent", `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"sub-1","result":null}}`, true},
		{"wrong subscription", `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"sub-2","result":{}}}`, false},
		{"empty subscription", `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"","result":{}}}`, false},
		{"numeric subscription", `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":1,"result":{}}}`, false},
		{"missing result", `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"sub-1","other":{}}}`, false},
		{"response instead of notification", `{"jsonrpc":"2.0","id":1,"result":{}}`, false},
		{"notification with id", `{"jsonrpc":"2.0","id":1,"method":"eth_subscription","params":{"subscription":"sub-1","result":{}}}`, false},
		{"wrong version", `{"jsonrpc":"1.0","method":"eth_subscription","params":{"subscription":"sub-1","result":{}}}`, false},
		{"wrong method", `{"jsonrpc":"2.0","method":"newHeads","params":{"subscription":"sub-1","result":{}}}`, false},
		{"extra params", `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"sub-1","result":{},"other":1}}`, false},
		{"batch", `[]`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value, err := strictjson.Parse([]byte(test.body))
			require.NoError(t, err)
			_, err = SubscriptionResult(value, "sub-1")
			assert.Equal(t, test.valid, err == nil)
		})
	}
}
