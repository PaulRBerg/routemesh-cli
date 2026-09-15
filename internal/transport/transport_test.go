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

func TestDoRPCReportsAllBatchCorrelationIDs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		headers http.Header
		ids     []any
	}{
		{name: "absent", ids: []any{nil}},
		{name: "singular", headers: http.Header{"X-Batch-Id": {"single"}}, ids: []any{"single"}},
		{name: "plural", headers: http.Header{"X-Batch-Ids": {"first, second", "third"}}, ids: []any{"first", "second", "third"}},
		{name: "both", headers: http.Header{"X-Batch-Id": {"request"}, "X-Batch-Ids": {"first, second"}}, ids: []any{"request", "first", "second"}},
		{name: "empty", headers: http.Header{"X-Batch-Ids": {" , "}}, ids: []any{nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var ids []any
			client := New(Options{
				APIKey: "secret",
				HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
					res := response(http.StatusOK, `[{"jsonrpc":"2.0","id":1,"result":"0x89"},{"jsonrpc":"2.0","id":2,"result":"0x1"}]`, nil)
					res.Header = tc.headers
					return res, nil
				}),
				Diagnostic: func(event any) {
					fields, ok := event.(map[string]any)
					require.True(t, ok)
					ids = append(ids, fields["batch_id"])
				},
			})
			envelope, err := jsonrpc.ParseRaw([]byte(`[{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]},{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]}]`))
			require.NoError(t, err)
			_, err = client.DoRPC(context.Background(), "137", envelope)
			require.NoError(t, err)
			assert.Equal(t, tc.ids, ids)
		})
	}
}

func TestDoRPCRetriesTransientHTTPWithRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 21, 20, 0, 0, 0, time.UTC)
	responses := []responseSpec{
		{status: http.StatusTooManyRequests, body: "rate limited", headers: map[string]string{"Retry-After": now.Add(3 * time.Second).Format(http.TimeFormat)}},
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
		Now: func() time.Time { return now },
		Rand: func() float64 {
			t.Fatal("valid Retry-After used random backoff")
			return 0
		},
	})
	result, err := client.DoRPC(context.Background(), "1", generated(t, "eth_chainId"))
	require.NoError(t, err)
	assert.Equal(t, 2, result.Attempts)
	assert.Equal(t, 2, calls)
	assert.Equal(t, []time.Duration{3 * time.Second}, slept)
}

func TestDoRPCRetriesTransientHTTPWithJitteredBackoff(t *testing.T) {
	t.Parallel()

	responses := []responseSpec{
		{status: http.StatusServiceUnavailable, body: "temporarily unavailable"},
		{status: http.StatusServiceUnavailable, body: "temporarily unavailable"},
		{status: http.StatusOK, body: `{"jsonrpc":"2.0","id":1,"result":"0x1"}`},
	}
	fractions := []float64{0.25, 0.75}
	calls := 0
	randCalls := 0
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
		Rand: func() float64 {
			fraction := fractions[randCalls]
			randCalls++
			return fraction
		},
	})
	result, err := client.DoRPC(context.Background(), "1", generated(t, "eth_chainId"))
	require.NoError(t, err)
	assert.Equal(t, 3, result.Attempts)
	assert.Equal(t, 3, calls)
	assert.Equal(t, 2, randCalls)
	assert.Equal(t, []time.Duration{250 * time.Millisecond, 1500 * time.Millisecond}, slept)
}

func TestDoRPCExhaustsTransientHTTPRetriesAsHTTPError(t *testing.T) {
	t.Parallel()

	calls := 0
	var slept []time.Duration
	client := New(Options{
		APIKey: "secret",
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return response(http.StatusServiceUnavailable, "temporarily unavailable", nil), nil
		}),
		Sleep: func(_ context.Context, delay time.Duration) error {
			slept = append(slept, delay)
			return nil
		},
		Rand: func() float64 { return 1 },
	})
	result, err := client.DoRPC(context.Background(), "1", generated(t, "eth_chainId"))
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Transport, typed.ExitCode)
	assert.Equal(t, "http_error", typed.Kind)
	assert.Equal(t, "RouteMesh returned HTTP 503 with no valid JSON-RPC response", typed.Message)
	assert.Equal(t, 3, result.Attempts)
	assert.Equal(t, http.StatusServiceUnavailable, result.HTTPStatus)
	assert.Equal(t, 3, calls)
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, slept)
}

func TestHTTPRetryDelayRejectsRetryAfterOverCap(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 21, 20, 0, 0, 0, time.UTC)
	delay := httpRetryDelay(now.Add(31*time.Second).Format(http.TimeFormat), now, 1, func() float64 { return 0.5 })
	assert.Equal(t, 500*time.Millisecond, delay)
}

func TestRetryableHTTPStatusesAreExact(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		assert.True(t, isRetryableHTTPStatus(status), status)
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError, http.StatusNotImplemented} {
		assert.False(t, isRetryableHTTPStatus(status), status)
	}
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

func TestDoRPCNeverRetriesTransientHTTPForWrites(t *testing.T) {
	t.Parallel()

	calls := 0
	client := New(Options{
		APIKey: "secret",
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return response(http.StatusTooManyRequests, "rate limited", nil), nil
		}),
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("write request attempted to sleep for a retry")
			return nil
		},
		Rand: func() float64 {
			t.Fatal("write request attempted to calculate retry jitter")
			return 0
		},
	})
	result, err := client.DoRPC(context.Background(), "1", generated(t, "eth_sendRawTransaction"))
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Transport, typed.ExitCode)
	assert.Equal(t, "http_error", typed.Kind)
	assert.Equal(t, 1, result.Attempts)
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
		Rand: func() float64 { return 1 },
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
		Rand: func() float64 { return 1 },
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

	calls := 0
	client := New(Options{
		APIKey: "sentinel-secret",
		HTTPClient: doerFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return response(http.StatusUnauthorized, "not-json", nil), nil
		}),
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("unauthorized response attempted to sleep for a retry")
			return nil
		},
	})
	_, err := client.DoRPC(context.Background(), "1", generated(t, "eth_chainId"))
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Credential, typed.ExitCode)
	assert.NotContains(t, typed.Error(), "sentinel-secret")
	assert.Equal(t, 1, calls)
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
