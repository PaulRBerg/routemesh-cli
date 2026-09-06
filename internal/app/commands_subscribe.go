package app

import (
	"github.com/paulrberg/routemesh-cli/internal/evidence"
	"github.com/paulrberg/routemesh-cli/internal/evm"
	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/output"
	"github.com/paulrberg/routemesh-cli/internal/transport"
)

func (command *SubscribeCmd) Run(runtime *Runtime) error {
	if _, err := evm.ParseChainID(command.ChainID); err != nil {
		return failure.Wrap(failure.Validation, "invalid_chain_id", err.Error(), err)
	}
	if command.Count < 1 || command.Count > transport.MaxSubscriptionCount {
		return failure.Validationf("invalid_count", "count must be between 1 and %d", transport.MaxSubscriptionCount)
	}
	var data []byte
	if command.JSON != "" {
		var err error
		data, err = rawInput(runtime, command.JSON)
		if err != nil {
			return err
		}
	}
	subscription, err := evidence.ParseSubscription(command.Subscription, data)
	if err != nil {
		return failure.Wrap(failure.Validation, "invalid_subscription", err.Error(), err)
	}
	request, err := subscription.Request()
	if err != nil {
		return failure.Wrap(failure.Validation, "invalid_subscription", err.Error(), err)
	}
	destination, err := transport.RedactedWebSocketDestination(runtime.rpcBase, command.ChainID)
	if err != nil {
		return err
	}
	if command.DryRun {
		return runtime.emitContract("subscribe", "dry_run", output.Document{JSON: map[string]any{
			"dry_run":     true,
			"chain_id":    command.ChainID,
			"destination": destination,
			"request":     request.Value(),
			"count":       command.Count,
			"reconnect":   false,
		}})
	}
	key, _, err := runtime.credential()
	if err != nil {
		return err
	}
	notifications, err := runtime.authenticatedClient(key).Subscribe(
		runtime.ctx, command.ChainID, request, command.Count, subscription.ValidateResult,
	)
	if err != nil {
		return err
	}
	return runtime.emitContract("subscribe", "output", output.Document{JSON: notifications, NDJSON: notifications})
}
