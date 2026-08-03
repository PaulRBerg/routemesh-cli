// Package jsonrpc validates and normalizes the CLI's JSON-RPC envelopes.
package jsonrpc

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/paulrberg/routemesh-cli/internal/strictjson"
)

const MaxBatchRequests = 100

var (
	methodPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	integerID     = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)$`)
)

type Request struct {
	JSONRPC string
	Method  string
	Params  any
	ID      any
}

type Envelope struct {
	Requests []Request
	Batch    bool
}

func Generated(method string, params any) (Envelope, error) {
	if err := ValidateMethod(method); err != nil {
		return Envelope{}, err
	}
	if params == nil {
		params = []any{}
	}
	if err := validateParams(params); err != nil {
		return Envelope{}, err
	}
	return Envelope{Requests: []Request{{JSONRPC: "2.0", Method: method, Params: params, ID: json.Number("1")}}}, nil
}

func Batch(requests ...Request) (Envelope, error) {
	if len(requests) == 0 || len(requests) > MaxBatchRequests {
		return Envelope{}, fmt.Errorf("JSON-RPC batch must contain 1 to %d requests", MaxBatchRequests)
	}
	seen := make(map[string]struct{}, len(requests))
	for i := range requests {
		if err := validateRequest(requests[i]); err != nil {
			return Envelope{}, fmt.Errorf("request %d: %w", i, err)
		}
		key, err := idKey(requests[i].ID)
		if err != nil {
			return Envelope{}, fmt.Errorf("request %d: %w", i, err)
		}
		if _, exists := seen[key]; exists {
			return Envelope{}, fmt.Errorf("duplicate JSON-RPC batch id at request %d", i)
		}
		seen[key] = struct{}{}
	}
	return Envelope{Requests: requests, Batch: true}, nil
}

func ParseRaw(data []byte) (Envelope, error) {
	value, err := strictjson.Parse(data)
	if err != nil {
		return Envelope{}, err
	}
	switch typed := value.(type) {
	case map[string]any:
		request, requestErr := requestFromMap(typed)
		if requestErr != nil {
			return Envelope{}, requestErr
		}
		return Envelope{Requests: []Request{request}}, nil
	case []any:
		if len(typed) == 0 || len(typed) > MaxBatchRequests {
			return Envelope{}, fmt.Errorf("JSON-RPC batch must contain 1 to %d requests", MaxBatchRequests)
		}
		requests := make([]Request, len(typed))
		for i, item := range typed {
			object, ok := item.(map[string]any)
			if !ok {
				return Envelope{}, fmt.Errorf("request %d: JSON-RPC request must be an object", i)
			}
			requests[i], err = requestFromMap(object)
			if err != nil {
				return Envelope{}, fmt.Errorf("request %d: %w", i, err)
			}
		}
		return Batch(requests...)
	default:
		return Envelope{}, fmt.Errorf("raw JSON-RPC input must be an object or non-empty batch")
	}
}

func ParseParams(data []byte) (any, error) {
	value, err := strictjson.Parse(data)
	if err != nil {
		return nil, err
	}
	if err := validateParams(value); err != nil {
		return nil, err
	}
	return value, nil
}

func requestFromMap(object map[string]any) (Request, error) {
	for key := range object {
		switch key {
		case "jsonrpc", "method", "params", "id":
		default:
			return Request{}, fmt.Errorf("unknown JSON-RPC request field %q", key)
		}
	}
	version, ok := object["jsonrpc"].(string)
	if !ok || version != "2.0" {
		return Request{}, fmt.Errorf("jsonrpc must be exactly %q", "2.0")
	}
	method, ok := object["method"].(string)
	if !ok {
		return Request{}, fmt.Errorf("method must be a string")
	}
	params, exists := object["params"]
	if !exists {
		params = []any{}
	}
	id, exists := object["id"]
	if !exists {
		return Request{}, fmt.Errorf("id is required; notifications are not supported")
	}
	request := Request{JSONRPC: version, Method: method, Params: params, ID: id}
	if err := validateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func validateRequest(request Request) error {
	if request.JSONRPC != "2.0" {
		return fmt.Errorf("jsonrpc must be exactly %q", "2.0")
	}
	if err := ValidateMethod(request.Method); err != nil {
		return err
	}
	if err := validateParams(request.Params); err != nil {
		return err
	}
	_, err := idKey(request.ID)
	return err
}

func validateParams(params any) error {
	switch params.(type) {
	case []any, map[string]any:
		return nil
	default:
		return fmt.Errorf("params must be a complete JSON array or object")
	}
}

func ValidateMethod(method string) error {
	if len(method) == 0 || len(method) > 128 {
		return fmt.Errorf("method must contain 1 to 128 bytes")
	}
	if !methodPattern.MatchString(method) {
		return fmt.Errorf("method contains a forbidden character")
	}
	return nil
}

func idKey(id any) (string, error) {
	switch typed := id.(type) {
	case string:
		return "s:" + typed, nil
	case json.Number:
		raw := typed.String()
		if !integerID.MatchString(raw) {
			return "", fmt.Errorf("id must be a string or integer")
		}
		value, ok := new(big.Int).SetString(raw, 10)
		if !ok {
			return "", fmt.Errorf("id must be a string or integer")
		}
		return "n:" + value.String(), nil
	default:
		return "", fmt.Errorf("id must be a string or integer")
	}
}

func EqualID(left, right any) bool {
	leftKey, leftErr := idKey(left)
	rightKey, rightErr := idKey(right)
	return leftErr == nil && rightErr == nil && leftKey == rightKey
}

func (e Envelope) Value() any {
	requests := make([]any, len(e.Requests))
	for i, request := range e.Requests {
		requests[i] = map[string]any{
			"id":      request.ID,
			"jsonrpc": request.JSONRPC,
			"method":  request.Method,
			"params":  request.Params,
		}
	}
	if e.Batch {
		return requests
	}
	return requests[0]
}

func (e Envelope) HasWrite() bool {
	for _, request := range e.Requests {
		if IsWriteMethod(request.Method) {
			return true
		}
	}
	return false
}

func IsWriteMethod(method string) bool {
	exact := map[string]struct{}{
		"eth_newBlockFilter":              {},
		"eth_newFilter":                   {},
		"eth_newPendingTransactionFilter": {},
		"eth_sendRawTransaction":          {},
		"eth_sendTransaction":             {},
		"eth_sign":                        {},
		"eth_signTransaction":             {},
		"eth_subscribe":                   {},
		"eth_uninstallFilter":             {},
		"eth_unsubscribe":                 {},
	}
	if _, ok := exact[method]; ok {
		return true
	}
	prefixes := []string{
		"admin_", "anvil_", "engine_", "evm_", "ganache_", "hardhat_",
		"miner_", "parity_set", "personal_", "wallet_",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(method, prefix) {
			return true
		}
	}
	return strings.HasPrefix(method, "eth_send") ||
		strings.HasPrefix(method, "eth_submit") ||
		strings.HasPrefix(method, "eth_signTypedData") ||
		strings.HasPrefix(method, "debug_set")
}
