package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (fn doerFunc) Do(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func response(status int, body string, headers map[string]string) *http.Response {
	header := make(http.Header)
	for key, value := range headers {
		header.Set(key, value)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

type responseSpec struct {
	status  int
	body    string
	headers map[string]string
}

func generated(t *testing.T, method string) jsonrpc.Envelope {
	t.Helper()
	envelope, err := jsonrpc.Generated(method, []any{})
	require.NoError(t, err)
	return envelope
}

func TestDoRPCUsesCredentialOnlyInTransportAndRedactsDiagnostics(t *testing.T) {
	t.Parallel()

	const secret = "sentinel/secret?#"
	var requestedURL string
	var events []any
	client := New(Options{
		APIKey:  secret,
		RPCBase: "https://rpc.example.test",
		HTTPClient: doerFunc(func(request *http.Request) (*http.Response, error) {
			requestedURL = request.URL.String()
			return response(http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`, map[string]string{"X-Batch-Id": "batch-1"}), nil
		}),
		Diagnostic: func(event any) { events = append(events, event) },
	})
	result, err := client.DoRPC(context.Background(), "1", generated(t, "eth_chainId"))
	require.NoError(t, err)
	assert.False(t, result.HasError)
	assert.Contains(t, requestedURL, "sentinel%2Fsecret%3F%23")
	require.Len(t, events, 1)
	encoded, err := json.Marshal(events)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), secret)
	assert.Contains(t, string(encoded), "batch-1")
	event, ok := events[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://rpc.example.test/rpc/1/<redacted>", event["destination"])
}

func TestDoRPCRetriesDocumentedReadOnlyErrorOnce(t *testing.T) {
	t.Parallel()

	responses := []responseSpec{
		{status: http.StatusTooManyRequests, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32003,"message":"cooldown"}}`, headers: map[string]string{"X-Batch-Id": "first", "Retry-After": "1"}},
		{status: http.StatusOK, body: `{"jsonrpc":"2.0","id":1,"result":"0x1"}`, headers: map[string]string{"X-Batch-Id": "second"}},
	}
	calls := 0
	var slept []time.Duration
	var events []any
	client := New(Options{
		APIKey: "secret",
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			item := responses[calls]
			calls++
			return response(item.status, item.body, item.headers), nil
		}),
		Sleep: func(_ context.Context, delay time.Duration) error {
			slept = append(slept, delay)
			return nil
		},
		Diagnostic: func(event any) { events = append(events, event) },
	})
	result, err := client.DoRPC(context.Background(), "1", generated(t, "eth_chainId"))
	require.NoError(t, err)
	assert.Equal(t, 2, result.Attempts)
	assert.Equal(t, []time.Duration{time.Second}, slept)
	assert.Len(t, events, 2)
}

func TestDoRPCHonorsZeroRetryAfter(t *testing.T) {
	t.Parallel()

	responses := []responseSpec{
		{status: http.StatusTooManyRequests, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32003,"message":"cooldown"}}`, headers: map[string]string{"Retry-After": "0"}},
		{status: http.StatusOK, body: `{"jsonrpc":"2.0","id":1,"result":"0x1"}`},
	}
	calls := 0
	var slept []time.Duration
	client := New(Options{
		APIKey: "secret",
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			item := responses[calls]
			calls++
			return response(item.status, item.body, item.headers), nil
		}),
		Sleep: func(_ context.Context, delay time.Duration) error {
			slept = append(slept, delay)
			return nil
		},
	})
	_, err := client.DoRPC(context.Background(), "1", generated(t, "eth_chainId"))
	require.NoError(t, err)
	assert.Equal(t, []time.Duration{0}, slept)
}

func TestDoRPCNeverRetriesAllowedWrites(t *testing.T) {
	t.Parallel()

	calls := 0
	client := New(Options{
		APIKey: "secret",
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return response(http.StatusTooManyRequests, `{"jsonrpc":"2.0","id":1,"error":{"code":-32003,"message":"cooldown"}}`, nil), nil
		}),
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("write request attempted to sleep for a retry")
			return nil
		},
	})
	result, err := client.DoRPC(context.Background(), "1", generated(t, "eth_sendRawTransaction"))
	require.NoError(t, err)
	assert.True(t, result.HasError)
	assert.Equal(t, 1, calls)
}

func TestDoRPCDoesNotRetryPartialBatch(t *testing.T) {
	t.Parallel()

	envelope, err := jsonrpc.Batch(
		jsonrpc.Request{JSONRPC: "2.0", Method: "eth_chainId", Params: []any{}, ID: json.Number("1")},
		jsonrpc.Request{JSONRPC: "2.0", Method: "eth_blockNumber", Params: []any{}, ID: json.Number("2")},
	)
	require.NoError(t, err)
	calls := 0
	client := New(Options{
		APIKey: "secret",
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return response(http.StatusOK, `[{"jsonrpc":"2.0","id":1,"result":"0x1"},{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"failed"}}]`, nil), nil
		}),
	})
	result, err := client.DoRPC(context.Background(), "1", envelope)
	require.NoError(t, err)
	assert.True(t, result.HasError)
	assert.Equal(t, 1, calls)
}

func TestDoRPCRetriesBatchOnlyWhenEveryItemIsRetryable(t *testing.T) {
	t.Parallel()

	envelope, err := jsonrpc.Batch(
		jsonrpc.Request{JSONRPC: "2.0", Method: "eth_chainId", Params: []any{}, ID: json.Number("1")},
		jsonrpc.Request{JSONRPC: "2.0", Method: "eth_blockNumber", Params: []any{}, ID: json.Number("2")},
	)
	require.NoError(t, err)
	bodies := []string{
		`[{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"internal"}},{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"server"}}]`,
		`[{"jsonrpc":"2.0","id":1,"result":"0x1"},{"jsonrpc":"2.0","id":2,"result":"0x2"}]`,
	}
	calls := 0
	var slept []time.Duration
	client := New(Options{
		APIKey: "secret",
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			body := bodies[calls]
			calls++
			return response(http.StatusOK, body, nil), nil
		}),
		Sleep: func(_ context.Context, delay time.Duration) error {
			slept = append(slept, delay)
			return nil
		},
	})
	result, err := client.DoRPC(context.Background(), "1", envelope)
	require.NoError(t, err)
	assert.False(t, result.HasError)
	assert.Equal(t, 2, calls)
	assert.Equal(t, []time.Duration{250 * time.Millisecond}, slept)
}

func TestDoRPCCancellationDuringBackoffIsTransportFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	client := New(Options{
		APIKey: "secret",
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"server"}}`, nil), nil
		}),
		Sleep: func(context.Context, time.Duration) error {
			cancel()
			return ctx.Err()
		},
	})
	_, err := client.DoRPC(ctx, "1", generated(t, "eth_chainId"))
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Transport, typed.ExitCode)
}

func TestDoRPCClassifiesContradictoryAndTransportFailures(t *testing.T) {
	t.Parallel()

	t.Run("mismatched id", func(t *testing.T) {
		client := New(Options{
			APIKey: "secret",
			HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, `{"jsonrpc":"2.0","id":2,"result":"0x1"}`, nil), nil
			}),
		})
		_, err := client.DoRPC(context.Background(), "1", generated(t, "eth_chainId"))
		var typed *failure.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, failure.Evidence, typed.ExitCode)
	})

	t.Run("URL error is sanitized", func(t *testing.T) {
		const secret = "sentinel-secret"
		client := New(Options{
			APIKey: secret,
			HTTPClient: doerFunc(func(request *http.Request) (*http.Response, error) {
				return nil, &url.Error{Op: "Post", URL: request.URL.String(), Err: errors.New("down")}
			}),
		})
		_, err := client.DoRPC(context.Background(), "1", generated(t, "eth_chainId"))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
		var typed *failure.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, failure.Transport, typed.ExitCode)
	})
}

func TestDoRPCClassifiesUnauthorizedWithoutTrustingTheBody(t *testing.T) {
	t.Parallel()

	client := New(Options{
		APIKey: "sentinel-secret",
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusUnauthorized, "not-json", nil), nil
		}),
	})
	_, err := client.DoRPC(context.Background(), "1", generated(t, "eth_chainId"))
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Credential, typed.ExitCode)
	assert.NotContains(t, typed.Error(), "sentinel-secret")
}

func TestDoRPCRejectsUnsafeChainAtTransportBoundary(t *testing.T) {
	t.Parallel()

	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsafe chain reached HTTP transport")
		return nil, nil
	})
	client := New(Options{APIKey: "secret", HTTPClient: doer})
	_, err := client.DoRPC(context.Background(), "1/../../.ssh", generated(t, "eth_chainId"))
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Validation, typed.ExitCode)
}

func TestGetAPIRejectsMalformedEvidence(t *testing.T) {
	t.Parallel()

	client := New(Options{HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"a":1,"a":2}`, nil), nil
	})})
	_, _, err := client.GetAPI(context.Background(), "/health")
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Evidence, typed.ExitCode)
}
