package app

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRPCPreservesSimulationFieldsThroughHTTP(t *testing.T) {
	t.Parallel()

	fees := []struct {
		name string
		json string
	}{
		{name: "legacy", json: `"type":"0x0","gasPrice":"0x53636305c6"`},
		{name: "equal caps", json: `"type":"0x2","maxFeePerGas":"0x53636305c6","maxPriorityFeePerGas":"0x53636305c6"`},
	}
	for _, fee := range fees {
		params := `[{"from":"0x1111111111111111111111111111111111111111","to":"0x2222222222222222222222222222222222222222","gas":"0x5208","value":"0x2b21a110959dcb8","nonce":"0x1","data":"0x",` + fee.json + `},{"blockHash":"0x` + strings.Repeat("ab", 32) + `","requireCanonical":true}]`
		call := `{"jsonrpc":"2.0","id":1,"method":"eth_call","params":` + params + `}`
		estimate := `{"jsonrpc":"2.0","id":2,"method":"eth_estimateGas","params":` + params + `}`
		batch := `[` + call + `,` + estimate + `]`
		cases := []struct {
			name     string
			args     []string
			stdin    string
			expected string
			response string
		}{
			{name: "params", args: []string{"rpc", "137", "eth_call", "--params", params}, expected: call, response: `{"jsonrpc":"2.0","id":1,"result":"0x"}`},
			{name: "raw", args: []string{"rpc", "137", "--json", call}, expected: call, response: `{"jsonrpc":"2.0","id":1,"result":"0x"}`},
			{name: "batch stdin", args: []string{"rpc", "137", "--json", "-"}, stdin: batch, expected: batch, response: `[{"jsonrpc":"2.0","id":1,"result":"0x"},{"jsonrpc":"2.0","id":2,"result":"0x5208"}]`},
		}
		for _, tc := range cases {
			t.Run(fee.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				doer := &doerStub{do: func(request *http.Request) (*http.Response, error) {
					body, err := io.ReadAll(request.Body)
					require.NoError(t, err)
					assert.JSONEq(t, tc.expected, string(body))
					return httpResponse(http.StatusOK, tc.response, nil), nil
				}}
				result := execute(t, tc.args, Dependencies{
					HTTPClient: doer,
					Keychain:   &keychainStub{available: true, key: "test-key"},
					Stdin:      strings.NewReader(tc.stdin),
				})
				assert.Equal(t, 0, result.code, result.stderr)
				assert.Equal(t, 1, doer.calls)
			})
		}
	}
}
