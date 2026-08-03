// Package transport implements the RouteMesh HTTP boundary and retry policy.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/jsonrpc"
	"github.com/paulrberg/routemesh-cli/internal/strictjson"
)

const (
	DefaultAPIBase    = "https://api.routeme.sh"
	DefaultRPCBase    = "https://lb.routeme.sh"
	DefaultOpenAPIURL = "https://routeme.sh/docs/api-reference/openapi.json"
	MaxResponseBytes  = 32 << 20
)

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type Diagnostic func(any)
type Sleep func(context.Context, time.Duration) error

type Options struct {
	HTTPClient Doer
	APIBase    string
	RPCBase    string
	OpenAPIURL string
	APIKey     string
	Diagnostic Diagnostic
	Sleep      Sleep
	Now        func() time.Time
}

type Client struct {
	httpClient Doer
	apiBase    string
	rpcBase    string
	openAPIURL string
	apiKey     string
	diagnostic Diagnostic
	sleep      Sleep
	now        func() time.Time
}

type RPCResult struct {
	Value      any
	Batch      bool
	HasError   bool
	ErrorCodes []int64
	HTTPStatus int
	Attempts   int
	Latency    time.Duration
}

func New(options Options) *Client {
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	apiBase := valueOr(options.APIBase, DefaultAPIBase)
	rpcBase := valueOr(options.RPCBase, DefaultRPCBase)
	openAPIURL := valueOr(options.OpenAPIURL, DefaultOpenAPIURL)
	diagnostic := options.Diagnostic
	if diagnostic == nil {
		diagnostic = func(any) {}
	}
	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		httpClient: httpClient,
		apiBase:    strings.TrimRight(apiBase, "/"),
		rpcBase:    strings.TrimRight(rpcBase, "/"),
		openAPIURL: openAPIURL,
		apiKey:     options.APIKey,
		diagnostic: diagnostic,
		sleep:      sleep,
		now:        now,
	}
}

func (c *Client) GetAPI(ctx context.Context, endpoint string) (any, time.Duration, error) {
	if endpoint != "/health" && endpoint != "/chains" {
		return nil, 0, failure.Validationf("invalid_endpoint", "unsupported public API endpoint %q", endpoint)
	}
	return c.getJSON(ctx, c.apiBase+endpoint)
}

func (c *Client) GetOpenAPI(ctx context.Context) (any, time.Duration, error) {
	return c.getJSON(ctx, c.openAPIURL)
}

func (c *Client) getJSON(ctx context.Context, destination string) (any, time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, destination, nil)
	if err != nil {
		return nil, 0, failure.Wrap(failure.Transport, "transport_error", "create HTTP request", err)
	}
	request.Header.Set("Accept", "application/json")
	started := time.Now()
	response, err := c.httpClient.Do(request)
	latency := time.Since(started)
	if err != nil {
		return nil, latency, transportFailure(ctx, err)
	}
	body, readErr := readResponse(response)
	if readErr != nil {
		return nil, latency, readErr
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, latency, failure.New(failure.Transport, "http_error", fmt.Sprintf("RouteMesh returned HTTP %d", response.StatusCode))
	}
	value, parseErr := strictjson.ParseBounded(body, MaxResponseBytes)
	if parseErr != nil {
		return nil, latency, failure.Wrap(failure.Evidence, "invalid_response", "RouteMesh returned invalid JSON evidence", parseErr)
	}
	return value, latency, nil
}

func (c *Client) DoRPC(ctx context.Context, chainID string, envelope jsonrpc.Envelope) (RPCResult, error) {
	if c.apiKey == "" {
		return RPCResult{}, failure.New(failure.Credential, "credential_missing", "no RouteMesh API key is configured")
	}
	body, err := json.Marshal(envelope.Value())
	if err != nil {
		return RPCResult{}, failure.Wrap(failure.Validation, "invalid_request", "encode JSON-RPC request", err)
	}
	destination, redacted, err := c.rpcDestinations(chainID)
	if err != nil {
		return RPCResult{}, err
	}
	maxAttempts := 2
	if envelope.HasWrite() {
		maxAttempts = 1
	}
	started := time.Now()
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retryAfter, attemptErr := c.rpcAttempt(ctx, destination, redacted, body, envelope, attempt)
		result.Attempts = attempt
		result.Latency = time.Since(started)
		if attemptErr != nil {
			return result, attemptErr
		}
		delay, retry := retryDelay(result.ErrorCodes, result.HasError, retryAfter, c.now())
		if !retry || attempt == maxAttempts {
			return result, nil
		}
		if err := c.sleep(ctx, delay); err != nil {
			return RPCResult{}, transportFailure(ctx, err)
		}
	}
	panic("unreachable")
}

func (c *Client) rpcAttempt(
	ctx context.Context,
	destination string,
	redacted string,
	body []byte,
	envelope jsonrpc.Envelope,
	attempt int,
) (RPCResult, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, destination, bytes.NewReader(body))
	if err != nil {
		return RPCResult{}, "", failure.Wrap(failure.Transport, "transport_error", "create RPC request", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return RPCResult{}, "", transportFailure(ctx, err)
	}
	for _, batchID := range response.Header.Values("X-Batch-Id") {
		c.emitAttempt(attempt, redacted, batchID, response.StatusCode)
	}
	if len(response.Header.Values("X-Batch-Id")) == 0 {
		c.emitAttempt(attempt, redacted, nil, response.StatusCode)
	}
	responseBody, readErr := readResponse(response)
	if readErr != nil {
		return RPCResult{}, "", readErr
	}
	value, parseErr := strictjson.ParseBounded(responseBody, MaxResponseBytes)
	if parseErr != nil {
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return RPCResult{}, "", failure.New(failure.Transport, "http_error", fmt.Sprintf("RouteMesh returned HTTP %d with no valid JSON-RPC response", response.StatusCode))
		}
		return RPCResult{}, "", failure.Wrap(failure.Evidence, "invalid_rpc_response", "RouteMesh returned invalid JSON-RPC evidence", parseErr)
	}
	result, validateErr := validateResponse(value, envelope)
	if validateErr != nil {
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			if response.StatusCode == http.StatusUnauthorized {
				return RPCResult{}, "", failure.New(failure.Credential, "credential_rejected", "RouteMesh rejected the configured API key")
			}
			return RPCResult{}, "", failure.New(failure.Transport, "http_error", fmt.Sprintf("RouteMesh returned HTTP %d with no valid JSON-RPC response", response.StatusCode))
		}
		return RPCResult{}, "", failure.Wrap(failure.Evidence, "invalid_rpc_response", "RouteMesh returned contradictory JSON-RPC evidence", validateErr)
	}
	result.HTTPStatus = response.StatusCode
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if response.StatusCode == http.StatusUnauthorized {
			return RPCResult{}, "", failure.New(failure.Credential, "credential_rejected", "RouteMesh rejected the configured API key")
		}
		if !result.HasError {
			return RPCResult{}, "", failure.Evidencef("contradictory_response", "RouteMesh returned HTTP %d with successful JSON-RPC evidence", response.StatusCode)
		}
	}
	return result, response.Header.Get("Retry-After"), nil
}

func (c *Client) rpcDestinations(chainID string) (string, string, error) {
	base, err := url.Parse(c.rpcBase)
	if err != nil || base.Scheme != "https" && base.Scheme != "http" || base.Host == "" {
		return "", "", failure.New(failure.Validation, "invalid_transport_configuration", "RPC base URL is invalid")
	}
	pathPrefix := strings.TrimRight(base.Path, "/")
	rawPathPrefix := strings.TrimRight(base.EscapedPath(), "/")
	base.Path = pathPrefix + "/rpc/" + chainID + "/" + c.apiKey
	base.RawPath = rawPathPrefix + "/rpc/" + url.PathEscape(chainID) + "/" + url.PathEscape(c.apiKey)
	redacted := c.rpcBase + "/rpc/" + chainID + "/<redacted>"
	return base.String(), redacted, nil
}

func RedactedDestination(rpcBase, chainID string) string {
	return strings.TrimRight(valueOr(rpcBase, DefaultRPCBase), "/") + "/rpc/" + chainID + "/<redacted>"
}

func (c *Client) emitAttempt(attempt int, destination string, batchID any, status int) {
	c.diagnostic(map[string]any{
		"type":        "request_attempt",
		"attempt":     attempt,
		"destination": destination,
		"batch_id":    batchID,
		"http_status": status,
	})
}

func readResponse(response *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	closeErr := response.Body.Close()
	if err != nil {
		return nil, failure.Wrap(failure.Transport, "transport_error", "read RouteMesh response", err)
	}
	if closeErr != nil {
		return nil, failure.Wrap(failure.Transport, "transport_error", "close RouteMesh response", closeErr)
	}
	if len(body) > MaxResponseBytes {
		return nil, failure.New(failure.Evidence, "response_too_large", fmt.Sprintf("RouteMesh response exceeds %d bytes", MaxResponseBytes))
	}
	return body, nil
}

func retryDelay(codes []int64, hasError bool, retryAfter string, now time.Time) (time.Duration, bool) {
	if !hasError || len(codes) == 0 {
		return 0, false
	}
	delay := time.Duration(0)
	for _, code := range codes {
		switch code {
		case -32003:
			candidate := parseRetryAfter(retryAfter, now)
			if candidate <= 0 || candidate > 30*time.Second {
				candidate = 2 * time.Second
			}
			if candidate > delay {
				delay = candidate
			}
		case -32603, -32000:
			if 250*time.Millisecond > delay {
				delay = 250 * time.Millisecond
			}
		default:
			return 0, false
		}
	}
	return delay, true
}

func parseRetryAfter(raw string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0
	}
	return when.Sub(now)
}

func transportFailure(ctx context.Context, _ error) *failure.Error {
	if ctx.Err() != nil {
		return failure.Wrap(failure.Transport, "transport_error", "RouteMesh request canceled or timed out", ctx.Err())
	}
	return failure.New(failure.Transport, "transport_error", "RouteMesh transport request failed")
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func valueOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
