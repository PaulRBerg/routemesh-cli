package app

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/paulrberg/routemesh-cli/internal/auth"
	"github.com/paulrberg/routemesh-cli/internal/evm"
	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/jsonrpc"
	"github.com/paulrberg/routemesh-cli/internal/output"
	"github.com/paulrberg/routemesh-cli/internal/schema"
	"github.com/paulrberg/routemesh-cli/internal/transport"
)

func (command *SchemaCmd) Run(runtime *Runtime) error {
	if len(command.Command) == 0 {
		index, err := schema.Index()
		if err != nil {
			return failure.Wrap(failure.Validation, "schema_error", "load embedded schema index", err)
		}
		return runtime.emit(output.Document{JSON: index})
	}
	if len(command.Command) == 1 && command.Command[0] == "api" {
		value, _, err := runtime.publicClient().GetOpenAPI(runtime.ctx)
		if err != nil {
			return err
		}
		object, ok := value.(map[string]any)
		if !ok {
			return failure.Evidencef("invalid_openapi", "RouteMesh OpenAPI document is not an object")
		}
		if _, ok := object["openapi"].(string); !ok {
			return failure.Evidencef("invalid_openapi", "RouteMesh OpenAPI document has no version")
		}
		if _, ok := object["paths"].(map[string]any); !ok {
			return failure.Evidencef("invalid_openapi", "RouteMesh OpenAPI document has no paths object")
		}
		return runtime.emit(output.Document{JSON: value})
	}
	detail, err := schema.Detail(command.Command...)
	if err != nil {
		return failure.Wrap(failure.Validation, "schema_error", err.Error(), err)
	}
	return runtime.emit(output.Document{JSON: detail})
}

func (command *InitCmd) Run(runtime *Runtime) error {
	chainID, err := evm.ParseChainID(command.ChainID)
	if err != nil {
		return failure.Wrap(failure.Validation, "invalid_chain_id", err.Error(), err)
	}
	if command.DryRun {
		available := runtime.keychain != nil && runtime.keychain.Available()
		plan := map[string]any{
			"dry_run":       true,
			"side_effect":   "external_write",
			"platform":      runtime.keychain.Platform(),
			"security_tool": runtime.keychain.ToolPath(),
			"chain_id":      command.ChainID,
			"steps": []any{
				map[string]any{"action": "check_keychain_tool", "available": available},
				map[string]any{"action": "add_keychain_item", "service": auth.KeychainService, "account": auth.KeychainAccount, "secret_input": "keychain_prompt"},
				map[string]any{"action": "retrieve_keychain_item"},
				map[string]any{"action": "rpc_probe", "method": "eth_chainId", "destination": transport.RedactedDestination(runtime.rpcBase, command.ChainID)},
			},
		}
		if err := runtime.emit(output.Document{JSON: plan}); err != nil {
			return err
		}
		if !available {
			return failure.New(failure.Credential, "keychain_unavailable", "macOS Keychain is unavailable on this platform")
		}
		return nil
	}
	if runtime.keychain == nil || !runtime.keychain.Available() {
		return failure.New(failure.Credential, "keychain_unavailable", "macOS Keychain is unavailable on this platform")
	}
	if err := runtime.keychain.AddInteractive(runtime.ctx, runtime.stdin, runtime.stderr, runtime.stderr); err != nil {
		return failure.Wrap(failure.Credential, "keychain_error", "could not store the RouteMesh API key in Keychain", err)
	}
	key, err := runtime.keychain.Get(runtime.ctx)
	if err != nil {
		return failure.Wrap(failure.Credential, "keychain_error", "could not retrieve the stored RouteMesh API key", err)
	}
	envelope, err := jsonrpc.Generated("eth_chainId", []any{})
	if err != nil {
		return failure.Wrap(failure.Validation, "invalid_request", "build validation probe", err)
	}
	result, err := runtime.authenticatedClient(key).DoRPC(runtime.ctx, command.ChainID, envelope)
	if err != nil {
		return err
	}
	if result.HasError {
		return providerFailure(result)
	}
	returned, err := chainIDResult(result.Value)
	if err != nil {
		return err
	}
	if returned.Cmp(new(big.Int).SetUint64(chainID)) != 0 {
		return failure.Evidencef("chain_mismatch", "eth_chainId did not match the explicit chain ID")
	}
	return runtime.emit(output.Document{JSON: map[string]any{
		"initialized":   true,
		"chain_id":      command.ChainID,
		"active_source": "keychain",
	}})
}

func (*AuthStatusCmd) Run(runtime *Runtime) error {
	environmentState := "not_configured"
	if runtime.getenv(auth.EnvironmentVariable) != "" {
		environmentState = "configured"
	}
	keychainState := "unavailable"
	if runtime.keychain != nil && runtime.keychain.Available() {
		_, err := runtime.keychain.Get(runtime.ctx)
		switch {
		case err == nil:
			keychainState = "configured"
		case errors.Is(err, auth.ErrNotFound):
			keychainState = "not_configured"
		default:
			keychainState = "error"
		}
	}
	activeSource := "none"
	if environmentState == "configured" {
		activeSource = "environment"
	} else if keychainState == "configured" {
		activeSource = "keychain"
	}
	return runtime.emit(output.Document{JSON: map[string]any{
		"environment":   environmentState,
		"keychain":      keychainState,
		"active_source": activeSource,
	}})
}

func (command *AuthClearCmd) Run(runtime *Runtime) error {
	if command.DryRun {
		return runtime.emit(output.Document{JSON: map[string]any{
			"dry_run":     true,
			"side_effect": "external_write",
			"action":      "delete_keychain_item",
			"environment": "unchanged",
		}})
	}
	if runtime.keychain == nil || !runtime.keychain.Available() {
		return failure.New(failure.Credential, "keychain_unavailable", "macOS Keychain is unavailable on this platform")
	}
	if _, err := runtime.keychain.Delete(runtime.ctx); err != nil {
		return failure.Wrap(failure.Credential, "keychain_error", "could not clear the RouteMesh Keychain item", err)
	}
	return runtime.emit(output.Document{JSON: map[string]any{
		"cleared":     true,
		"environment": "unchanged",
	}})
}

func chainIDResult(value any) (*big.Int, error) {
	result, err := singleResult(value)
	if err != nil {
		return nil, err
	}
	quantity, ok := result.(string)
	if !ok {
		return nil, failure.Evidencef("invalid_chain_id_result", "eth_chainId result is not a string")
	}
	parsed, err := evm.ParseBigQuantity(quantity)
	if err != nil {
		return nil, failure.Wrap(failure.Evidence, "invalid_chain_id_result", "eth_chainId returned a malformed quantity", err)
	}
	return parsed, nil
}

func singleResult(value any) (any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, failure.Evidencef("invalid_rpc_response", "JSON-RPC response is not an object")
	}
	result, exists := object["result"]
	if !exists {
		return nil, failure.Evidencef("invalid_rpc_response", "JSON-RPC response has no result")
	}
	return result, nil
}

func providerFailure(result transport.RPCResult) *failure.Error {
	codes := make([]string, len(result.ErrorCodes))
	for i, code := range result.ErrorCodes {
		codes[i] = fmt.Sprintf("%d", code)
	}
	details := map[string]any{}
	if len(codes) > 0 {
		details["codes"] = strings.Join(codes, ",")
	}
	return failure.WithDetails(
		failure.New(failure.Provider, "provider_error", "RouteMesh returned a final JSON-RPC error"),
		details,
	)
}
