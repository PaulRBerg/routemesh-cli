package app

import (
	"fmt"

	"github.com/paulrberg/routemesh-cli/internal/evidence"
	"github.com/paulrberg/routemesh-cli/internal/evm"
	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/output"
	"github.com/paulrberg/routemesh-cli/internal/transport"
)

func (command *LogsCmd) Run(runtime *Runtime) error {
	if _, err := evm.ParseChainID(command.ChainID); err != nil {
		return failure.Wrap(failure.Validation, "invalid_chain_id", err.Error(), err)
	}
	data, err := rawInput(runtime, command.JSON)
	if err != nil {
		return err
	}
	filter, err := evidence.ParseLogFilter(data)
	if err != nil {
		return failure.Wrap(failure.Validation, "invalid_log_filter", err.Error(), err)
	}
	if command.DryRun {
		var (
			chunks            any
			runtimeResolution any
		)
		if filter.ToLatest {
			chunks = nil
			runtimeResolution = map[string]any{
				"field":  "toBlock",
				"method": "eth_blockNumber",
				"value":  "latest",
			}
		} else {
			count, countErr := filter.ChunkCount(filter.To)
			if countErr != nil {
				return failure.Wrap(failure.Validation, "invalid_log_range", countErr.Error(), countErr)
			}
			if count > uint64((runtime.output.MaxBytes/32)+1) {
				return maxOutputPlanError(count, runtime.output.MaxBytes)
			}
			planned, planErr := filter.Chunks(filter.To)
			if planErr != nil {
				return failure.Wrap(failure.Validation, "invalid_log_range", planErr.Error(), planErr)
			}
			values := make([]any, len(planned))
			for i, chunk := range planned {
				values[i] = chunk.Value()
			}
			chunks = values
			runtimeResolution = nil
		}
		return runtime.emit(output.Document{JSON: map[string]any{
			"dry_run":            true,
			"chain_id":           command.ChainID,
			"destination":        transport.RedactedDestination(runtime.rpcBase, command.ChainID),
			"filter":             filter.Canonical(),
			"chunk_size":         evidence.LogChunkSize,
			"chunks":             chunks,
			"runtime_resolution": runtimeResolution,
		}})
	}
	key, _, err := runtime.credential()
	if err != nil {
		return err
	}
	result, err := evidence.CollectLogs(runtime.ctx, runtime.authenticatedClient(key), command.ChainID, filter)
	if err != nil {
		return err
	}
	return runtime.emit(output.Document{JSON: result.Value(), NDJSON: result.Records()})
}

func (command *ReceiptCmd) Run(runtime *Runtime) error {
	if _, err := evm.ParseChainID(command.ChainID); err != nil {
		return failure.Wrap(failure.Validation, "invalid_chain_id", err.Error(), err)
	}
	if err := evm.ValidateHash(command.TxHash); err != nil {
		return failure.Wrap(failure.Validation, "invalid_transaction_hash", err.Error(), err)
	}
	if command.DryRun {
		return runtime.emit(output.Document{JSON: map[string]any{
			"dry_run":          true,
			"chain_id":         command.ChainID,
			"transaction_hash": command.TxHash,
			"destination":      transport.RedactedDestination(runtime.rpcBase, command.ChainID),
			"requests": []any{
				map[string]any{"method": "eth_getTransactionByHash", "params": []any{command.TxHash}, "when": "always"},
				map[string]any{"method": "eth_getTransactionReceipt", "params": []any{command.TxHash}, "when": "after transaction proves an exact block"},
				map[string]any{"method": "eth_getBlockReceipts", "params": []any{"<transaction.blockNumber>"}, "when": "direct receipt is null"},
				map[string]any{"method": "eth_getBlockByHash", "params": []any{"<transaction.blockHash>", false}, "when": "after one receipt is verified"},
			},
		}})
	}
	key, _, err := runtime.credential()
	if err != nil {
		return err
	}
	result, err := evidence.CollectReceipt(runtime.ctx, runtime.authenticatedClient(key), command.ChainID, command.TxHash)
	if err != nil {
		return err
	}
	return runtime.emit(output.Document{JSON: result.Value()})
}

func maxOutputPlanError(chunks uint64, limit int64) error {
	return failure.New(
		failure.Validation,
		"output_limit",
		fmt.Sprintf("log chunk plan with %d chunks cannot fit within %d output bytes", chunks, limit),
	)
}
