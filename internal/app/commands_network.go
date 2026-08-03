package app

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"

	"github.com/paulrberg/routemesh-cli/internal/evm"
	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/jsonrpc"
	"github.com/paulrberg/routemesh-cli/internal/output"
	"github.com/paulrberg/routemesh-cli/internal/strictjson"
	"github.com/paulrberg/routemesh-cli/internal/transport"
)

func (*HealthCmd) Run(runtime *Runtime) error {
	value, latency, err := runtime.publicClient().GetAPI(runtime.ctx, "/health")
	if err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 2 {
		return failure.Evidencef("invalid_health", "RouteMesh health response has an unexpected shape")
	}
	ready, ok := object["success"].(bool)
	if !ok {
		return failure.Evidencef("invalid_health", "RouteMesh health response has no boolean success state")
	}
	message, ok := object["message"].(string)
	if !ok {
		return failure.Evidencef("invalid_health", "RouteMesh health response has no message")
	}
	document := output.Document{JSON: map[string]any{
		"ready":      ready,
		"message":    message,
		"latency_ms": latency.Milliseconds(),
	}}
	if err := runtime.emitContract("health", "output", document); err != nil {
		return err
	}
	if !ready {
		return failure.Evidencef("service_unavailable", "RouteMesh reported that the service is not ready")
	}
	return nil
}

func (*ChainsCmd) Run(runtime *Runtime) error {
	value, _, err := runtime.publicClient().GetAPI(runtime.ctx, "/chains")
	if err != nil {
		return err
	}
	items, ok := value.([]any)
	if !ok {
		return failure.Evidencef("invalid_chains", "RouteMesh chain catalog is not an array")
	}
	type chain struct {
		id    uint64
		value map[string]any
	}
	chains := make([]chain, len(items))
	seen := make(map[uint64]struct{}, len(items))
	for i, item := range items {
		object, ok := item.(map[string]any)
		if !ok || len(object) != 2 {
			return failure.Evidencef("invalid_chains", "RouteMesh chain catalog item %d has an unexpected shape", i)
		}
		chainID, ok := object["chain_id"].(string)
		if !ok {
			return failure.Evidencef("invalid_chains", "RouteMesh chain catalog item %d has no chain_id", i)
		}
		id, err := evm.ParseChainID(chainID)
		if err != nil {
			return failure.Wrap(failure.Evidence, "invalid_chains", fmt.Sprintf("RouteMesh chain catalog item %d has an invalid chain_id", i), err)
		}
		if _, duplicate := seen[id]; duplicate {
			return failure.Evidencef("contradictory_chains", "RouteMesh chain catalog contains duplicate chain ID %d", id)
		}
		seen[id] = struct{}{}
		if _, ok := object["name"].(string); !ok {
			return failure.Evidencef("invalid_chains", "RouteMesh chain catalog item %d has no name", i)
		}
		chains[i] = chain{id: id, value: object}
	}
	sort.Slice(chains, func(i, j int) bool { return chains[i].id < chains[j].id })
	values := make([]any, len(chains))
	for i := range chains {
		values[i] = chains[i].value
	}
	return runtime.emitContract("chains", "output", output.Document{JSON: values, NDJSON: values})
}

func (command *PingCmd) Run(runtime *Runtime) error {
	chainID, err := evm.ParseChainID(command.ChainID)
	if err != nil {
		return failure.Wrap(failure.Validation, "invalid_chain_id", err.Error(), err)
	}
	envelope, err := jsonrpc.Batch(
		jsonrpc.Request{JSONRPC: "2.0", Method: "eth_chainId", Params: []any{}, ID: json.Number("1")},
		jsonrpc.Request{JSONRPC: "2.0", Method: "eth_blockNumber", Params: []any{}, ID: json.Number("2")},
	)
	if err != nil {
		return failure.Wrap(failure.Validation, "invalid_request", "build ping request", err)
	}
	key, _, err := runtime.credential()
	if err != nil {
		return err
	}
	result, err := runtime.authenticatedClient(key).DoRPC(runtime.ctx, command.ChainID, envelope)
	if err != nil {
		return err
	}
	if result.HasError {
		return providerFailure(result)
	}
	responses, ok := result.Value.([]any)
	if !ok || len(responses) != 2 {
		return failure.Evidencef("invalid_ping", "ping returned an invalid batch")
	}
	returnedChain, err := chainIDResult(responses[0])
	if err != nil {
		return err
	}
	if returnedChain.Cmp(new(big.Int).SetUint64(chainID)) != 0 {
		return failure.Evidencef("chain_mismatch", "eth_chainId did not match the explicit chain ID")
	}
	blockResult, err := singleResult(responses[1])
	if err != nil {
		return err
	}
	blockHex, ok := blockResult.(string)
	if !ok {
		return failure.Evidencef("invalid_block_number", "eth_blockNumber result is not a string")
	}
	block, err := evm.ParseBigQuantity(blockHex)
	if err != nil {
		return failure.Wrap(failure.Evidence, "invalid_block_number", "eth_blockNumber returned a malformed quantity", err)
	}
	return runtime.emitContract("ping", "output", output.Document{JSON: map[string]any{
		"chain_id":         command.ChainID,
		"block_number":     block.String(),
		"block_number_hex": blockHex,
		"latency_ms":       result.Latency.Milliseconds(),
		"routes":           []string{"eth_chainId", "eth_blockNumber"},
	}})
}

func (command *RPCCmd) Run(runtime *Runtime) error {
	if _, err := evm.ParseChainID(command.ChainID); err != nil {
		return failure.Wrap(failure.Validation, "invalid_chain_id", err.Error(), err)
	}
	envelope, err := command.envelope(runtime)
	if err != nil {
		return err
	}
	if envelope.HasWrite() && !command.AllowWrite {
		return failure.New(failure.Validation, "write_not_allowed", "request contains a signing or mutation method; pass --allow-write to opt in")
	}
	if command.DryRun {
		sideEffect := "read_only"
		if envelope.HasWrite() {
			sideEffect = "external_write"
		}
		return runtime.emitContract("rpc", "dry_run", output.Document{JSON: map[string]any{
			"dry_run":     true,
			"chain_id":    command.ChainID,
			"destination": transport.RedactedDestination(runtime.rpcBase, command.ChainID),
			"side_effect": sideEffect,
			"request":     envelope.Value(),
			"retry": map[string]any{
				"eligible":     !envelope.HasWrite(),
				"max_attempts": map[bool]int{true: 1, false: 2}[envelope.HasWrite()],
			},
		}})
	}
	key, _, err := runtime.credential()
	if err != nil {
		return err
	}
	result, err := runtime.authenticatedClient(key).DoRPC(runtime.ctx, command.ChainID, envelope)
	if err != nil {
		return err
	}
	document := output.Document{JSON: result.Value}
	if result.Batch {
		document.NDJSON, _ = result.Value.([]any)
	}
	if err := runtime.emitContract("rpc", "output", document); err != nil {
		return err
	}
	if result.HasError {
		return providerFailure(result)
	}
	return nil
}

func (command *RPCCmd) envelope(runtime *Runtime) (jsonrpc.Envelope, error) {
	if command.JSON != "" {
		if command.Method != "" || command.Params != "" {
			return jsonrpc.Envelope{}, failure.New(failure.Validation, "conflicting_input", "--json is mutually exclusive with METHOD and --params")
		}
		data, err := rawInput(runtime, command.JSON)
		if err != nil {
			return jsonrpc.Envelope{}, err
		}
		envelope, err := jsonrpc.ParseRaw(data)
		if err != nil {
			return jsonrpc.Envelope{}, failure.Wrap(failure.Validation, "invalid_json_rpc", err.Error(), err)
		}
		return envelope, nil
	}
	if command.Method == "" {
		return jsonrpc.Envelope{}, failure.New(failure.Validation, "missing_method", "METHOD is required unless --json is used")
	}
	params := any([]any{})
	if command.Params != "" {
		parsed, err := jsonrpc.ParseParams([]byte(command.Params))
		if err != nil {
			return jsonrpc.Envelope{}, failure.Wrap(failure.Validation, "invalid_params", err.Error(), err)
		}
		params = parsed
	}
	envelope, err := jsonrpc.Generated(command.Method, params)
	if err != nil {
		return jsonrpc.Envelope{}, failure.Wrap(failure.Validation, "invalid_json_rpc", err.Error(), err)
	}
	return envelope, nil
}

func rawInput(runtime *Runtime, value string) ([]byte, error) {
	if value == "-" {
		data, err := strictjson.Read(runtime.stdin, strictjson.MaxInputBytes)
		if err != nil {
			return nil, failure.Wrap(failure.Validation, "invalid_json_input", err.Error(), err)
		}
		return data, nil
	}
	if len(value) > strictjson.MaxInputBytes {
		return nil, failure.New(failure.Validation, "invalid_json_input", fmt.Sprintf("JSON input exceeds %d bytes", strictjson.MaxInputBytes))
	}
	return []byte(value), nil
}
