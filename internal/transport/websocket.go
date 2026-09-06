package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"

	"github.com/paulrberg/routemesh-cli/internal/evm"
	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/jsonrpc"
	"github.com/paulrberg/routemesh-cli/internal/strictjson"
)

const MaxSubscriptionCount = 1000

type WebSocketConn interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close() error
}

type WebSocketDial func(context.Context, string) (WebSocketConn, *http.Response, error)

type socketConn struct{ conn *websocket.Conn }

func dialWebSocket(ctx context.Context, destination string) (WebSocketConn, *http.Response, error) {
	conn, response, err := websocket.Dial(ctx, destination, &websocket.DialOptions{
		HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	})
	if err != nil {
		return nil, response, err
	}
	conn.SetReadLimit(MaxResponseBytes)
	return &socketConn{conn: conn}, response, nil
}

func (c *socketConn) Read(ctx context.Context) ([]byte, error) {
	kind, data, err := c.conn.Read(ctx)
	if err != nil {
		if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
			return nil, failure.New(failure.Evidence, "response_too_large", "WebSocket message exceeds the response byte limit")
		}
		return nil, transportFailure(ctx, err)
	}
	if kind != websocket.MessageText {
		return nil, failure.New(failure.Evidence, "invalid_notification", "WebSocket JSON-RPC messages must be text")
	}
	return data, nil
}

func (c *socketConn) Write(ctx context.Context, data []byte) error {
	return c.conn.Write(ctx, websocket.MessageText, data)
}

func (c *socketConn) Close() error {
	// Closing the connection ends its subscriptions without extending the command deadline.
	return c.conn.CloseNow()
}

// Subscribe collects a finite set of validated notifications on one connection, without reconnecting.
func (c *Client) Subscribe(
	ctx context.Context,
	chainID string,
	request jsonrpc.Envelope,
	count int,
	validateResult func(any) error,
) ([]any, error) {
	if _, err := evm.ParseChainID(chainID); err != nil {
		return nil, failure.Wrap(failure.Validation, "invalid_chain_id", err.Error(), err)
	}
	if count < 1 || count > MaxSubscriptionCount {
		return nil, failure.Validationf("invalid_count", "count must be between 1 and %d", MaxSubscriptionCount)
	}
	if request.Batch || len(request.Requests) != 1 || request.Requests[0].Method != "eth_subscribe" {
		return nil, failure.New(failure.Validation, "invalid_subscription", "expected one eth_subscribe request")
	}
	destination, redacted, err := webSocketDestinations(c.rpcBase, chainID, c.apiKey)
	if err != nil {
		return nil, err
	}
	if c.apiKey == "" {
		return nil, failure.New(failure.Credential, "credential_missing", "no RouteMesh API key is configured")
	}
	body, err := json.Marshal(request.Value())
	if err != nil {
		return nil, failure.Wrap(failure.Validation, "invalid_subscription", "encode subscription request", err)
	}
	dial := c.webSocketDial
	if dial == nil {
		dial = dialWebSocket
	}
	socket, response, err := dial(ctx, destination)
	if response != nil {
		if response.Body != nil {
			defer func() { _ = response.Body.Close() }()
		}
		var sessionID any
		if value := response.Header.Get("X-WebSocket-Session-ID"); value != "" {
			sessionID = strings.ReplaceAll(value, c.apiKey, "<redacted>")
		}
		c.diagnostic(map[string]any{
			"type": "websocket_session", "destination": redacted,
			"session_id": sessionID, "http_status": response.StatusCode,
		})
	}
	if err != nil {
		if response != nil {
			switch response.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return nil, failure.New(failure.Credential, "credential_rejected", "RouteMesh rejected the WebSocket credentials or origin")
			case http.StatusPaymentRequired:
				return nil, failure.New(failure.Credential, "insufficient_credits", "RouteMesh reported insufficient WebSocket credits")
			}
		}
		return nil, transportFailure(ctx, err)
	}
	defer func() { _ = socket.Close() }()
	if err := socket.Write(ctx, body); err != nil {
		return nil, transportFailure(ctx, err)
	}
	remaining := MaxResponseBytes
	value, err := readSocketJSON(ctx, socket, &remaining)
	if err != nil {
		return nil, err
	}
	result, err := validateResponse(value, request)
	if err != nil {
		return nil, failure.Wrap(failure.Evidence, "invalid_subscription_response", "WebSocket subscription response is invalid", err)
	}
	if result.HasError {
		return nil, failure.WithDetails(
			failure.New(failure.Provider, "provider_error", "RouteMesh rejected the subscription"),
			map[string]any{"rpc_codes": result.ErrorCodes},
		)
	}
	id, ok := value.(map[string]any)["result"].(string)
	if !ok || id == "" {
		return nil, failure.New(failure.Evidence, "invalid_subscription_response", "WebSocket subscription response has no subscription ID")
	}
	notifications := make([]any, 0, count)
	for range count {
		value, err := readSocketJSON(ctx, socket, &remaining)
		if err != nil {
			return nil, err
		}
		payload, err := jsonrpc.SubscriptionResult(value, id)
		if err != nil {
			return nil, failure.Wrap(failure.Evidence, "invalid_notification", "WebSocket notification does not match the active subscription", err)
		}
		if validateResult != nil {
			if err := validateResult(payload); err != nil {
				return nil, err
			}
		}
		notifications = append(notifications, value)
	}
	return notifications, nil
}

func readSocketJSON(ctx context.Context, socket WebSocketConn, remaining *int) (any, error) {
	data, err := socket.Read(ctx)
	if err != nil {
		var typed *failure.Error
		if errors.As(err, &typed) {
			return nil, typed
		}
		return nil, transportFailure(ctx, err)
	}
	*remaining -= len(data)
	if *remaining < 0 {
		return nil, failure.New(failure.Evidence, "response_too_large", "WebSocket subscription exceeds the total response byte limit")
	}
	value, err := strictjson.ParseBounded(data, MaxResponseBytes)
	if err != nil {
		return nil, failure.Wrap(failure.Evidence, "invalid_notification", "WebSocket response contains invalid JSON evidence", err)
	}
	return value, nil
}

func RedactedWebSocketDestination(rpcBase, chainID string) (string, error) {
	_, redacted, err := webSocketDestinations(rpcBase, chainID, "")
	return redacted, err
}

func webSocketDestinations(rpcBase, chainID, apiKey string) (string, string, error) {
	base, err := url.Parse(valueOr(rpcBase, DefaultRPCBase))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", "", failure.New(failure.Validation, "invalid_transport_configuration", "WebSocket base URL is invalid")
	}
	switch base.Scheme {
	case "https", "wss":
		base.Scheme = "wss"
	case "http", "ws":
		base.Scheme = "ws"
	default:
		return "", "", failure.New(failure.Validation, "invalid_transport_configuration", "WebSocket base URL must use HTTP(S) or WS(S)")
	}
	redacted := strings.TrimRight(base.String(), "/") + "/rpc/" + chainID + "/<redacted>"
	prefix := strings.TrimRight(base.Path, "/")
	escapedPrefix := strings.TrimRight(base.EscapedPath(), "/")
	base.Path = prefix + "/rpc/" + chainID + "/" + apiKey
	base.RawPath = escapedPrefix + "/rpc/" + url.PathEscape(chainID) + "/" + url.PathEscape(apiKey)
	return base.String(), redacted, nil
}
