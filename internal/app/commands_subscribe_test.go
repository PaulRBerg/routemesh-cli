package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulrberg/routemesh-cli/internal/schema"
	"github.com/paulrberg/routemesh-cli/internal/transport"
)

type subscriptionSocket struct {
	messages []string
	writes   [][]byte
	closed   bool
}

func (s *subscriptionSocket) Read(context.Context) ([]byte, error) {
	if len(s.messages) == 0 {
		return nil, errors.New("connection dropped")
	}
	message := s.messages[0]
	s.messages = s.messages[1:]
	return []byte(message), nil
}

func (s *subscriptionSocket) Write(_ context.Context, data []byte) error {
	s.writes = append(s.writes, data)
	return nil
}

func (s *subscriptionSocket) Close() error { s.closed = true; return nil }

func notificationJSON(t *testing.T, result any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "eth_subscription",
		"params": map[string]any{"subscription": "sub-1", "result": result},
	})
	require.NoError(t, err)
	return string(data)
}

func headNotification(t *testing.T) string {
	t.Helper()
	return notificationJSON(t, map[string]any{
		"number": "0x1", "hash": "0x" + strings.Repeat("a", 64), "parentHash": "0x" + strings.Repeat("b", 64),
	})
}

func socketDependencies(t *testing.T, socket *subscriptionSocket) Dependencies {
	t.Helper()
	return Dependencies{
		Getenv:     func(string) string { return "secret-key" },
		HTTPClient: &doerStub{},
		WebSocketDial: func(_ context.Context, destination string) (transport.WebSocketConn, *http.Response, error) {
			assert.Equal(t, "wss://lb.routeme.sh/rpc/1/secret-key", destination)
			return socket, &http.Response{StatusCode: 101, Header: http.Header{"X-Websocket-Session-Id": []string{"test-session"}}}, nil
		},
	}
}

func TestSubscribeValidatesBeforeCredentialsOrNetwork(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"subscribe", "01", "newHeads"},
		{"subscribe", "0", "newHeads"},
		{"subscribe", "1", "unknown"},
		{"subscribe", "1", "newHeads", "--count=0"},
		{"subscribe", "1", "newHeads", "--count=-1"},
		{"subscribe", "1", "newHeads", "--count=1001"},
		{"subscribe", "1", "newHeads", "--json={}"},
		{"subscribe", "1", "newPendingTransactions", "--json={}"},
		{"subscribe", "1", "logs", "--json=[]"},
		{"subscribe", "1", "logs", "--json={"},
		{"subscribe", "1", "logs", "--json={\"fromBlock\":\"0x1\"}"},
		{"subscribe", "1", "logs", "--json={\"address\":\"0x1\"}"},
		{"subscribe", "1", "logs", "--json={\"topics\":[],\"topics\":[]}"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			keychain := &keychainStub{available: true, key: "secret"}
			httpClient := &doerStub{}
			credentialReads := 0
			dials := 0
			result := execute(t, args, Dependencies{
				Keychain: keychain, HTTPClient: httpClient,
				Getenv: func(string) string { credentialReads++; return "" },
				WebSocketDial: func(context.Context, string) (transport.WebSocketConn, *http.Response, error) {
					dials++
					return nil, nil, errors.New("unexpected dial")
				},
			})
			assert.Equal(t, 2, result.code)
			assert.Empty(t, result.stdout)
			assert.Zero(t, keychain.gets)
			assert.Zero(t, credentialReads)
			assert.Zero(t, httpClient.calls)
			assert.Zero(t, dials)
		})
	}
}

func TestSubscribeDryRunPlansValidatedRequestWithoutSideEffects(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"newHeads", "logs", "newPendingTransactions"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			keychain := &keychainStub{available: true, key: "secret"}
			args := []string{"subscribe", "1", kind, "--count=3", "--dry-run"}
			if kind == "logs" {
				args = append(args, "--json=-")
			}
			result := execute(t, args, Dependencies{
				Keychain: keychain, HTTPClient: &doerStub{}, Stdin: strings.NewReader(`{"topics":[null]}`),
				Getenv: func(string) string { t.Error("unexpected credential lookup"); return "" },
				WebSocketDial: func(context.Context, string) (transport.WebSocketConn, *http.Response, error) {
					t.Error("unexpected dial")
					return nil, nil, errors.New("unexpected dial")
				},
			})
			require.Equal(t, 0, result.code, result.stderr)
			plan := decodeObject(t, result.stdout)
			assert.Equal(t, "wss://lb.routeme.sh/rpc/1/<redacted>", plan["destination"])
			assert.Equal(t, float64(3), plan["count"])
			assert.Equal(t, false, plan["reconnect"])
			request := plan["request"].(map[string]any)
			assert.Equal(t, "eth_subscribe", request["method"])
			assert.Equal(t, kind, request["params"].([]any)[0])
			assert.Zero(t, keychain.gets)
			assert.Zero(t, keychain.adds)
			assert.Empty(t, result.stderr)
		})
	}
}

func TestSubscribeJSONAndNDJSONAreBoundedAndSelectable(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"json", "ndjson"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			socket := &subscriptionSocket{messages: []string{
				`{"jsonrpc":"2.0","id":1,"result":"sub-1"}`, headNotification(t), headNotification(t),
			}}
			result := execute(t, []string{"--output=" + format, "subscribe", "1", "newHeads", "--count=2"}, socketDependencies(t, socket))
			require.Equal(t, 0, result.code, result.stderr)
			if format == "json" {
				var values []any
				require.NoError(t, json.Unmarshal([]byte(result.stdout), &values))
				assert.Len(t, values, 2)
			} else {
				assert.Len(t, decodeLines(t, result.stdout), 2)
			}
			assert.True(t, socket.closed)
			assert.NotContains(t, result.stdout+result.stderr, "secret-key")
			require.NoError(t, schema.ValidateDefinition("subscribe", "stderr_event", decodeObject(t, result.stderr)))
		})
	}

	socket := &subscriptionSocket{messages: []string{`{"jsonrpc":"2.0","id":1,"result":"sub-1"}`, headNotification(t)}}
	result := execute(t, []string{"--max-output-bytes=7", "--select=/0/params/result/number", "subscribe", "1", "newHeads"}, socketDependencies(t, socket))
	require.Equal(t, 0, result.code, result.stderr)
	assert.Equal(t, "\"0x1\"\n", result.stdout)

	socket = &subscriptionSocket{messages: []string{`{"jsonrpc":"2.0","id":1,"result":"sub-1"}`, headNotification(t)}}
	result = execute(t, []string{"--max-output-bytes=7", "subscribe", "1", "newHeads"}, socketDependencies(t, socket))
	assert.Equal(t, 2, result.code)
	assert.Empty(t, result.stdout)
}

func TestSubscribeFailureNeverEmitsPartialNotifications(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		last string
		code int
	}{
		{"disconnect", "", 4},
		{"malformed JSON", `{`, 6},
		{"malformed head", notificationJSON(t, map[string]any{"number": "0x1"}), 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			socket := &subscriptionSocket{messages: []string{`{"jsonrpc":"2.0","id":1,"result":"sub-1"}`, headNotification(t)}}
			if test.last != "" {
				socket.messages = append(socket.messages, test.last)
			}
			result := execute(t, []string{"subscribe", "1", "newHeads", "--count=2"}, socketDependencies(t, socket))
			assert.Equal(t, test.code, result.code)
			assert.Empty(t, result.stdout)
			assert.True(t, socket.closed)
		})
	}
}

func TestSubscribeLogsPreserveRemovedEvents(t *testing.T) {
	t.Parallel()

	address := "0x" + strings.Repeat("a", 40)
	hash := "0x" + strings.Repeat("b", 64)
	log := map[string]any{
		"address": address, "topics": []any{}, "data": "0x", "removed": true,
		"blockHash": hash, "blockNumber": "0x1", "transactionHash": hash, "transactionIndex": "0x0", "logIndex": "0x0",
	}
	socket := &subscriptionSocket{messages: []string{`{"jsonrpc":"2.0","id":1,"result":"sub-1"}`, notificationJSON(t, log)}}
	result := execute(t, []string{"--output=ndjson", "subscribe", "1", "logs", "--json={\"address\":\"" + address + "\"}"}, socketDependencies(t, socket))
	require.Equal(t, 0, result.code, result.stderr)
	value := decodeObject(t, result.stdout)["params"].(map[string]any)["result"].(map[string]any)
	assert.Equal(t, true, value["removed"])
	assert.Contains(t, string(socket.writes[0]), address)
}

func TestChainsSelectsTransportWithoutCredentialAccess(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"rpc", "ws"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			doer := &doerStub{do: func(request *http.Request) (*http.Response, error) {
				assert.Equal(t, "/chains/"+mode, request.URL.Path)
				assert.Equal(t, http.MethodGet, request.Method)
				return httpResponse(200, `[{"chain_id":"10","name":"Ten"},{"chain_id":"1","name":"One"}]`, nil), nil
			}}
			result := execute(t, []string{"chains", "--transport=" + mode, "--select=/0/chain_id"}, Dependencies{
				HTTPClient: doer, Getenv: func(string) string { t.Error("unexpected credential access"); return "" },
			})
			require.Equal(t, 0, result.code, result.stderr)
			assert.Equal(t, "\"1\"\n", result.stdout)
		})
	}
}

func TestSubscribeInvalidConfigurationDoesNotReadCredentials(t *testing.T) {
	t.Parallel()

	keychain := &keychainStub{available: true, key: "secret"}
	result := execute(t, []string{"subscribe", "1", "newHeads"}, Dependencies{Keychain: keychain, RPCBase: "https://user:secret@example.test"})
	assert.Equal(t, 2, result.code)
	assert.Zero(t, keychain.gets)
	assert.NotContains(t, result.stderr, "secret")
}
