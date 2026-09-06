package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulrberg/routemesh-cli/internal/failure"
)

const subscriptionAck = `{"jsonrpc":"2.0","id":1,"result":"sub-1"}`
const subscriptionEvent = `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"sub-1","result":"event"}}`

type socketStub struct {
	messages []string
	writes   [][]byte
	closed   bool
	readErr  error
}

func (s *socketStub) Read(context.Context) ([]byte, error) {
	if len(s.messages) == 0 {
		if s.readErr != nil {
			return nil, s.readErr
		}
		return nil, io.EOF
	}
	value := s.messages[0]
	s.messages = s.messages[1:]
	return []byte(value), nil
}

func (s *socketStub) Write(_ context.Context, data []byte) error {
	s.writes = append(s.writes, data)
	return nil
}

func (s *socketStub) Close() error {
	s.closed = true
	return nil
}

func TestSubscribeCollectsExactCountAndClosesConnection(t *testing.T) {
	t.Parallel()

	socket := &socketStub{messages: []string{subscriptionAck, subscriptionEvent, subscriptionEvent, "unread"}}
	var diagnostics []any
	calls := 0
	validated := 0
	client := New(Options{
		APIKey:     "key/with?parts",
		Diagnostic: func(event any) { diagnostics = append(diagnostics, event) },
		WebSocketDial: func(_ context.Context, destination string) (WebSocketConn, *http.Response, error) {
			calls++
			assert.Equal(t, "wss://lb.routeme.sh/rpc/1/key%2Fwith%3Fparts", destination)
			return socket, &http.Response{StatusCode: 101, Header: http.Header{"X-Websocket-Session-Id": []string{"session-1"}}}, nil
		},
	})
	values, err := client.Subscribe(context.Background(), "1", generated(t, "eth_subscribe"), 2, func(value any) error {
		validated++
		assert.Equal(t, "event", value)
		return nil
	})
	require.NoError(t, err)
	assert.Len(t, values, 2)
	assert.Equal(t, 2, validated)
	assert.Equal(t, 1, calls)
	assert.True(t, socket.closed)
	assert.Equal(t, []string{"unread"}, socket.messages)
	require.Len(t, socket.writes, 1)
	assert.Contains(t, string(socket.writes[0]), `"method":"eth_subscribe"`)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, map[string]any{
		"type": "websocket_session", "destination": "wss://lb.routeme.sh/rpc/1/<redacted>",
		"session_id": "session-1", "http_status": 101,
	}, diagnostics[0])
}

func TestSubscribeRejectsInvalidOrIncompleteEvidence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		messages []string
		code     int
	}{
		{"wrong ack id", []string{`{"jsonrpc":"2.0","id":2,"result":"sub-1"}`}, failure.Evidence},
		{"ack error", []string{`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"secret-key"}}`}, failure.Provider},
		{"empty subscription id", []string{`{"jsonrpc":"2.0","id":1,"result":""}`}, failure.Evidence},
		{"numeric subscription id", []string{`{"jsonrpc":"2.0","id":1,"result":1}`}, failure.Evidence},
		{"duplicate key", []string{subscriptionAck, `{"jsonrpc":"2.0","jsonrpc":"2.0"}`}, failure.Evidence},
		{"wrong notification id", []string{subscriptionAck, strings.Replace(subscriptionEvent, "sub-1", "sub-2", 1)}, failure.Evidence},
		{"missing result", []string{subscriptionAck, `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"sub-1"}}`}, failure.Evidence},
		{"disconnect before count", []string{subscriptionAck, subscriptionEvent}, failure.Transport},
		{"ack batch", []string{`[` + subscriptionAck + `]`}, failure.Evidence},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			socket := &socketStub{messages: test.messages, readErr: errors.New("secret-key in transport error")}
			client := New(Options{APIKey: "secret-key", WebSocketDial: func(context.Context, string) (WebSocketConn, *http.Response, error) {
				return socket, nil, nil
			}})
			values, err := client.Subscribe(context.Background(), "1", generated(t, "eth_subscribe"), 2, nil)
			var typed *failure.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, test.code, typed.ExitCode)
			assert.NotContains(t, typed.Message, "secret-key")
			assert.Nil(t, values)
			assert.True(t, socket.closed)
		})
	}
}

func TestSubscribeHandshakeFailuresAreNotRetried(t *testing.T) {
	t.Parallel()

	for _, code := range []int{401, 402, 403, 429, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			t.Parallel()
			calls := 0
			client := New(Options{APIKey: "key", WebSocketDial: func(context.Context, string) (WebSocketConn, *http.Response, error) {
				calls++
				return nil, &http.Response{StatusCode: code}, errors.New("wss://example.test/rpc/1/key")
			}})
			_, err := client.Subscribe(context.Background(), "1", generated(t, "eth_subscribe"), 1, nil)
			var typed *failure.Error
			require.ErrorAs(t, err, &typed)
			expected := failure.Transport
			if code < 429 {
				expected = failure.Credential
			}
			assert.Equal(t, expected, typed.ExitCode)
			assert.Equal(t, 1, calls)
			assert.NotContains(t, err.Error(), "/key")
		})
	}
}

func TestSocketJSONEnforcesAggregateLimitAndPropagatesEvidenceFailures(t *testing.T) {
	t.Parallel()

	socket := &socketStub{messages: []string{`"one"`, `"two"`}}
	remaining := 8
	_, err := readSocketJSON(context.Background(), socket, &remaining)
	require.NoError(t, err)
	_, err = readSocketJSON(context.Background(), socket, &remaining)
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "response_too_large", typed.Kind)
	assert.Equal(t, failure.Evidence, typed.ExitCode)

	socket = &socketStub{readErr: failure.New(failure.Evidence, "invalid_notification", "binary message")}
	_, err = readSocketJSON(context.Background(), socket, &remaining)
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Evidence, typed.ExitCode)
}

func TestWebSocketDefaultDialUsesRealProtocolAndHonorsDeadline(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rpc/1/key", r.URL.Path)
		w.Header().Set("X-WebSocket-Session-ID", "session-local")
		conn, err := websocket.Accept(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, err = conn.Read(ctx)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(subscriptionAck))) {
			return
		}
		if !assert.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(subscriptionEvent))) {
			return
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	client := New(Options{APIKey: "key", RPCBase: server.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	values, err := client.Subscribe(ctx, "1", generated(t, "eth_subscribe"), 2, nil)
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Transport, typed.ExitCode)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, values)
}

func TestWebSocketRejectsBinaryFrames(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, err = conn.Read(ctx)
		if !assert.NoError(t, err) {
			return
		}
		assert.NoError(t, conn.Write(ctx, websocket.MessageBinary, []byte(subscriptionAck)))
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	client := New(Options{APIKey: "key", RPCBase: server.URL})
	_, err := client.Subscribe(context.Background(), "1", generated(t, "eth_subscribe"), 1, nil)
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Evidence, typed.ExitCode)
}

func TestWebSocketDoesNotFollowCredentialBearingRedirect(t *testing.T) {
	t.Parallel()

	var redirected atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/rpc/1/key", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := New(Options{APIKey: "key", RPCBase: server.URL})
	_, err := client.Subscribe(context.Background(), "1", generated(t, "eth_subscribe"), 1, nil)
	require.Error(t, err)
	assert.Zero(t, redirected.Load())
}

func TestWebSocketDestinationValidationAndRedaction(t *testing.T) {
	t.Parallel()

	redacted, err := RedactedWebSocketDestination("https://example.test/prefix/", "1")
	require.NoError(t, err)
	assert.Equal(t, "wss://example.test/prefix/rpc/1/<redacted>", redacted)
	for _, base := range []string{"ftp://example.test", "https:///", "https://user:secret@example.test", "https://example.test?key=secret", "https://example.test#secret"} {
		_, err := RedactedWebSocketDestination(base, "1")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "secret")
	}
}
