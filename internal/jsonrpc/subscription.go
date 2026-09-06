package jsonrpc

import "fmt"

// SubscriptionResult validates a notification for this connection's subscription.
func SubscriptionResult(value any, subscriptionID string) (any, error) {
	object, ok := value.(map[string]any)
	if !ok || len(object) != 3 || object["jsonrpc"] != "2.0" || object["method"] != "eth_subscription" {
		return nil, fmt.Errorf("expected a JSON-RPC eth_subscription notification")
	}
	params, ok := object["params"].(map[string]any)
	if !ok || len(params) != 2 {
		return nil, fmt.Errorf("subscription notification params must contain subscription and result")
	}
	id, ok := params["subscription"].(string)
	if !ok || id == "" || id != subscriptionID {
		return nil, fmt.Errorf("notification subscription ID does not match the active subscription")
	}
	result, exists := params["result"]
	if !exists {
		return nil, fmt.Errorf("subscription notification has no result")
	}
	return result, nil
}
