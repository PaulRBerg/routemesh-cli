package evidence

import (
	"fmt"
	"strings"

	"github.com/paulrberg/routemesh-cli/internal/evm"
	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/jsonrpc"
	"github.com/paulrberg/routemesh-cli/internal/strictjson"
)

type Subscription struct {
	Kind   string
	Filter map[string]any
}

func ParseSubscription(kind string, data []byte) (Subscription, error) {
	subscription := Subscription{Kind: kind}
	switch kind {
	case "newHeads", "newPendingTransactions":
		if len(data) != 0 {
			return Subscription{}, fmt.Errorf("--json is only accepted for logs subscriptions")
		}
		return subscription, nil
	case "logs":
	default:
		return Subscription{}, fmt.Errorf("subscription must be newHeads, logs, or newPendingTransactions")
	}
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	value, err := strictjson.Parse(data)
	if err != nil {
		return Subscription{}, err
	}
	filter, ok := value.(map[string]any)
	if !ok {
		return Subscription{}, fmt.Errorf("subscription log filter must be a JSON object")
	}
	for key, item := range filter {
		switch key {
		case "address":
			err = validateAddresses(item)
		case "topics":
			err = validateTopics(item)
		default:
			return Subscription{}, fmt.Errorf("subscription log filter only accepts address and topics; use logs for historical ranges")
		}
		if err != nil {
			return Subscription{}, err
		}
	}
	subscription.Filter = filter
	return subscription, nil
}

func (s Subscription) Request() (jsonrpc.Envelope, error) {
	params := []any{s.Kind}
	if s.Kind == "logs" {
		params = append(params, s.Filter)
	}
	return jsonrpc.Generated("eth_subscribe", params)
}

// ValidateResult checks event payloads without treating live observations as finalized evidence.
func (s Subscription) ValidateResult(value any) error {
	switch s.Kind {
	case "newHeads":
		object, ok := value.(map[string]any)
		if !ok {
			return failure.Evidencef("invalid_head", "newHeads notification is not an object")
		}
		number, ok := object["number"].(string)
		if !ok {
			return failure.Evidencef("invalid_head", "newHeads notification has no block number")
		}
		if _, err := evm.ParseQuantity(number); err != nil {
			return failure.Evidencef("invalid_head", "newHeads notification has an invalid block number")
		}
		for _, field := range []string{"hash", "parentHash"} {
			hash, ok := object[field].(string)
			if !ok || evm.ValidateHash(hash) != nil {
				return failure.Evidencef("invalid_head", "newHeads notification has an invalid %s", field)
			}
		}
	case "logs":
		object, _, err := validateLogFields(value)
		if err != nil {
			return err
		}
		if address, exists := s.Filter["address"]; exists && !matchesLogValue(address, object["address"].(string)) {
			return failure.Evidencef("contradictory_log", "subscription log address does not match the requested filter")
		}
		if topics, exists := s.Filter["topics"]; exists {
			actual := object["topics"].([]any)
			for i, expected := range topics.([]any) {
				if i >= len(actual) || !matchesLogValue(expected, actual[i].(string)) {
					return failure.Evidencef("contradictory_log", "subscription log topics do not match the requested filter")
				}
			}
		}
	case "newPendingTransactions":
		hash, ok := value.(string)
		if !ok || evm.ValidateHash(hash) != nil {
			return failure.Evidencef("invalid_pending_transaction", "pending transaction notification is not a transaction hash")
		}
	}
	return nil
}

func matchesLogValue(filter any, actual string) bool {
	switch expected := filter.(type) {
	case nil:
		return true
	case string:
		return strings.EqualFold(expected, actual)
	case []any:
		for _, item := range expected {
			if matchesLogValue(item, actual) {
				return true
			}
		}
	}
	return false
}
