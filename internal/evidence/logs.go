package evidence

import (
	"context"
	"strings"

	"github.com/paulrberg/routemesh-cli/internal/evm"
	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/jsonrpc"
	"github.com/paulrberg/routemesh-cli/internal/transport"
)

type RPCCaller interface {
	DoRPC(context.Context, string, jsonrpc.Envelope) (transport.RPCResult, error)
}

type Checkpoint struct {
	Phase       string `json:"phase"`
	BlockNumber string `json:"block_number"`
	BlockHash   string `json:"block_hash"`
}

type LogEvidence struct {
	ChainID     string
	From        uint64
	To          uint64
	UpperHash   string
	ChunkCount  int
	Checkpoints []Checkpoint
	Logs        []map[string]any
}

func CollectLogs(ctx context.Context, caller RPCCaller, chainID string, filter LogFilter) (LogEvidence, error) {
	upper := filter.To
	if filter.ToLatest {
		value, err := call(ctx, caller, chainID, "eth_blockNumber", []any{})
		if err != nil {
			return LogEvidence{}, err
		}
		raw, ok := value.(string)
		if !ok {
			return LogEvidence{}, failure.Evidencef("invalid_latest_block", "eth_blockNumber result is not a string")
		}
		upper, err = evm.ParseQuantity(raw)
		if err != nil {
			return LogEvidence{}, failure.Wrap(failure.Evidence, "invalid_latest_block", "eth_blockNumber returned a malformed quantity", err)
		}
	}
	if _, err := filter.ChunkCount(upper); err != nil {
		return LogEvidence{}, failure.Wrap(failure.Evidence, "unavailable_range", err.Error(), err)
	}
	before, err := fetchHeader(ctx, caller, chainID, upper, "before")
	if err != nil {
		return LogEvidence{}, err
	}
	logs := make([]map[string]any, 0)
	var previous *logPosition
	chunkCount := 0
	for start := filter.From; ; {
		end := upper
		if upper-start >= LogChunkSize {
			end = start + LogChunkSize - 1
		}
		chunk := Chunk{From: start, To: end}
		chunkCount++
		value, err := call(ctx, caller, chainID, "eth_getLogs", []any{filter.ForChunk(chunk)})
		if err != nil {
			return LogEvidence{}, err
		}
		items, ok := value.([]any)
		if !ok {
			return LogEvidence{}, failure.Evidencef("invalid_logs", "eth_getLogs result is not an array")
		}
		for _, item := range items {
			log, position, err := validateLog(item, chunk)
			if err != nil {
				return LogEvidence{}, err
			}
			if previous != nil && !previous.before(position) {
				return LogEvidence{}, failure.Evidencef("contradictory_logs", "eth_getLogs results are duplicated or out of order")
			}
			copy := position
			previous = &copy
			logs = append(logs, log)
		}
		if end == upper {
			break
		}
		start = end + 1
	}
	after, err := fetchHeader(ctx, caller, chainID, upper, "after")
	if err != nil {
		return LogEvidence{}, err
	}
	if !strings.EqualFold(before.BlockHash, after.BlockHash) {
		return LogEvidence{}, failure.Evidencef("reorg_detected", "upper-bound block hash changed during log collection")
	}
	return LogEvidence{
		ChainID:     chainID,
		From:        filter.From,
		To:          upper,
		UpperHash:   after.BlockHash,
		ChunkCount:  chunkCount,
		Checkpoints: []Checkpoint{before, after},
		Logs:        logs,
	}, nil
}

func call(ctx context.Context, caller RPCCaller, chainID, method string, params any) (any, error) {
	envelope, err := jsonrpc.Generated(method, params)
	if err != nil {
		return nil, failure.Wrap(failure.Validation, "invalid_request", "build evidence request", err)
	}
	result, err := caller.DoRPC(ctx, chainID, envelope)
	if err != nil {
		return nil, err
	}
	if result.HasError {
		return nil, failure.WithDetails(
			failure.New(failure.Provider, "provider_error", "RouteMesh returned a final JSON-RPC error"),
			map[string]any{"codes": result.ErrorCodes},
		)
	}
	object, ok := result.Value.(map[string]any)
	if !ok {
		return nil, failure.Evidencef("invalid_rpc_response", "%s response is not an object", method)
	}
	value, exists := object["result"]
	if !exists {
		return nil, failure.Evidencef("invalid_rpc_response", "%s response has no result", method)
	}
	return value, nil
}

func fetchHeader(ctx context.Context, caller RPCCaller, chainID string, block uint64, phase string) (Checkpoint, error) {
	value, err := call(ctx, caller, chainID, "eth_getBlockByNumber", []any{evm.Quantity(block), false})
	if err != nil {
		return Checkpoint{}, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return Checkpoint{}, failure.Evidencef("unavailable_block", "upper-bound block header is unavailable")
	}
	number, ok := object["number"].(string)
	if !ok {
		return Checkpoint{}, failure.Evidencef("invalid_block", "upper-bound block header has no number")
	}
	parsed, err := evm.ParseQuantity(number)
	if err != nil || parsed != block {
		return Checkpoint{}, failure.Evidencef("contradictory_block", "upper-bound block header number does not match the requested block")
	}
	hash, ok := object["hash"].(string)
	if !ok || evm.ValidateHash(hash) != nil {
		return Checkpoint{}, failure.Evidencef("invalid_block", "upper-bound block header has no full block hash")
	}
	return Checkpoint{Phase: phase, BlockNumber: number, BlockHash: hash}, nil
}

type logPosition struct {
	block uint64
	tx    uint64
	index uint64
}

func (p logPosition) before(other logPosition) bool {
	if p.block != other.block {
		return p.block < other.block
	}
	if p.tx != other.tx {
		return p.tx < other.tx
	}
	return p.index < other.index
}

func validateLog(value any, chunk Chunk) (map[string]any, logPosition, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, logPosition{}, failure.Evidencef("invalid_log", "log entry is not an object")
	}
	address, ok := object["address"].(string)
	if !ok || evm.ValidateAddress(address) != nil {
		return nil, logPosition{}, failure.Evidencef("invalid_log", "log entry has an invalid address")
	}
	topics, ok := object["topics"].([]any)
	if !ok || len(topics) > 4 {
		return nil, logPosition{}, failure.Evidencef("invalid_log", "log entry has invalid topics")
	}
	for _, topic := range topics {
		raw, ok := topic.(string)
		if !ok || evm.ValidateTopic(raw) != nil {
			return nil, logPosition{}, failure.Evidencef("invalid_log", "log entry has an invalid topic")
		}
	}
	data, ok := object["data"].(string)
	if !ok || evm.ValidateData(data) != nil {
		return nil, logPosition{}, failure.Evidencef("invalid_log", "log entry has invalid data")
	}
	block, err := quantityField(object, "blockNumber")
	if err != nil || block < chunk.From || block > chunk.To {
		return nil, logPosition{}, failure.Evidencef("contradictory_log", "log block number is outside its requested chunk")
	}
	txIndex, err := quantityField(object, "transactionIndex")
	if err != nil {
		return nil, logPosition{}, err
	}
	logIndex, err := quantityField(object, "logIndex")
	if err != nil {
		return nil, logPosition{}, err
	}
	transactionHash, ok := object["transactionHash"].(string)
	if !ok || evm.ValidateHash(transactionHash) != nil {
		return nil, logPosition{}, failure.Evidencef("invalid_log", "log entry has an invalid transaction hash")
	}
	blockHash, ok := object["blockHash"].(string)
	if !ok || evm.ValidateHash(blockHash) != nil {
		return nil, logPosition{}, failure.Evidencef("invalid_log", "log entry has an invalid block hash")
	}
	removed, ok := object["removed"].(bool)
	if !ok || removed {
		return nil, logPosition{}, failure.Evidencef("contradictory_log", "log entry is missing a non-removed state")
	}
	return object, logPosition{block: block, tx: txIndex, index: logIndex}, nil
}

func quantityField(object map[string]any, name string) (uint64, error) {
	raw, ok := object[name].(string)
	if !ok {
		return 0, failure.Evidencef("invalid_log", "log entry has no %s quantity", name)
	}
	value, err := evm.ParseQuantity(raw)
	if err != nil {
		return 0, failure.Evidencef("invalid_log", "log entry has an invalid %s quantity", name)
	}
	return value, nil
}

func CheckpointRecord(checkpoint Checkpoint) map[string]any {
	return map[string]any{
		"type":         "checkpoint",
		"phase":        checkpoint.Phase,
		"block_number": checkpoint.BlockNumber,
		"block_hash":   checkpoint.BlockHash,
	}
}

func LogRecord(index int, log map[string]any) map[string]any {
	return map[string]any{"type": "log", "index": index, "log": log}
}

func SummaryRecord(result LogEvidence) map[string]any {
	return map[string]any{
		"type":             "summary",
		"chain_id":         result.ChainID,
		"from_block":       evm.Quantity(result.From),
		"to_block":         evm.Quantity(result.To),
		"upper_bound_hash": result.UpperHash,
		"chunks":           result.ChunkCount,
		"log_count":        len(result.Logs),
		"canonical":        true,
	}
}

func (result LogEvidence) Value() map[string]any {
	checkpoints := make([]any, len(result.Checkpoints))
	for i, checkpoint := range result.Checkpoints {
		checkpoints[i] = checkpoint
	}
	logs := make([]any, len(result.Logs))
	for i, log := range result.Logs {
		logs[i] = log
	}
	return map[string]any{
		"chain_id":         result.ChainID,
		"from_block":       evm.Quantity(result.From),
		"to_block":         evm.Quantity(result.To),
		"upper_bound_hash": result.UpperHash,
		"chunks":           result.ChunkCount,
		"checkpoints":      checkpoints,
		"logs":             logs,
	}
}

func (result LogEvidence) Records() []any {
	records := make([]any, 0, len(result.Logs)+3)
	records = append(records, CheckpointRecord(result.Checkpoints[0]))
	for i, log := range result.Logs {
		records = append(records, LogRecord(i, log))
	}
	records = append(records, CheckpointRecord(result.Checkpoints[1]), SummaryRecord(result))
	return records
}
