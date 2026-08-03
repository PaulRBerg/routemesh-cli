package evidence

import (
	"context"
	"strings"

	"github.com/paulrberg/routemesh-cli/internal/evm"
	"github.com/paulrberg/routemesh-cli/internal/failure"
)

type ReceiptEvidence struct {
	ChainID         string
	TransactionHash string
	Source          string
	Transaction     map[string]any
	Receipt         map[string]any
	Block           map[string]any
}

func CollectReceipt(ctx context.Context, caller RPCCaller, chainID, transactionHash string) (ReceiptEvidence, error) {
	transactionValue, err := call(ctx, caller, chainID, "eth_getTransactionByHash", []any{transactionHash})
	if err != nil {
		return ReceiptEvidence{}, err
	}
	transaction, blockNumber, blockHash, err := validateTransaction(transactionValue, transactionHash)
	if err != nil {
		return ReceiptEvidence{}, err
	}
	receiptValue, err := call(ctx, caller, chainID, "eth_getTransactionReceipt", []any{transactionHash})
	if err != nil {
		return ReceiptEvidence{}, err
	}
	source := "direct"
	var receipt map[string]any
	if receiptValue == nil {
		source = "block_receipts"
		blockReceipts, callErr := call(ctx, caller, chainID, "eth_getBlockReceipts", []any{blockNumber})
		if callErr != nil {
			return ReceiptEvidence{}, callErr
		}
		receipt, err = matchingReceipt(blockReceipts, transactionHash, blockNumber, blockHash)
		if err != nil {
			return ReceiptEvidence{}, err
		}
	} else {
		receipt, err = validateReceipt(receiptValue, transactionHash, blockNumber, blockHash)
		if err != nil {
			return ReceiptEvidence{}, err
		}
	}
	blockValue, err := call(ctx, caller, chainID, "eth_getBlockByHash", []any{blockHash, false})
	if err != nil {
		return ReceiptEvidence{}, err
	}
	block, err := validateExactBlock(blockValue, blockNumber, blockHash)
	if err != nil {
		return ReceiptEvidence{}, err
	}
	return ReceiptEvidence{
		ChainID:         chainID,
		TransactionHash: transactionHash,
		Source:          source,
		Transaction:     transaction,
		Receipt:         receipt,
		Block:           block,
	}, nil
}

func validateTransaction(value any, expectedHash string) (map[string]any, string, string, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, "", "", failure.Evidencef("transaction_unavailable", "transaction is unavailable or not mined")
	}
	hash, ok := object["hash"].(string)
	if !ok || evm.ValidateHash(hash) != nil || !strings.EqualFold(hash, expectedHash) {
		return nil, "", "", failure.Evidencef("contradictory_transaction", "transaction hash does not match the requested transaction")
	}
	blockNumber, ok := object["blockNumber"].(string)
	if !ok {
		return nil, "", "", failure.Evidencef("transaction_unavailable", "transaction does not prove an exact block number")
	}
	if _, err := evm.ParseQuantity(blockNumber); err != nil {
		return nil, "", "", failure.Evidencef("invalid_transaction", "transaction has a malformed block number")
	}
	blockHash, ok := object["blockHash"].(string)
	if !ok || evm.ValidateHash(blockHash) != nil {
		return nil, "", "", failure.Evidencef("transaction_unavailable", "transaction does not prove an exact block hash")
	}
	return object, blockNumber, blockHash, nil
}

func validateReceipt(value any, expectedHash, blockNumber, blockHash string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, failure.Evidencef("invalid_receipt", "receipt is not an object")
	}
	hash, ok := object["transactionHash"].(string)
	if !ok || evm.ValidateHash(hash) != nil || !strings.EqualFold(hash, expectedHash) {
		return nil, failure.Evidencef("contradictory_receipt", "receipt transaction hash does not match")
	}
	receiptBlock, ok := object["blockNumber"].(string)
	if !ok || !equalQuantity(receiptBlock, blockNumber) {
		return nil, failure.Evidencef("contradictory_receipt", "receipt block number does not match the transaction")
	}
	receiptBlockHash, ok := object["blockHash"].(string)
	if !ok || evm.ValidateHash(receiptBlockHash) != nil || !strings.EqualFold(receiptBlockHash, blockHash) {
		return nil, failure.Evidencef("contradictory_receipt", "receipt block hash does not match the transaction")
	}
	transactionIndex, ok := object["transactionIndex"].(string)
	if !ok {
		return nil, failure.Evidencef("invalid_receipt", "receipt has no transaction index")
	}
	if _, err := evm.ParseQuantity(transactionIndex); err != nil {
		return nil, failure.Evidencef("invalid_receipt", "receipt has a malformed transaction index")
	}
	return object, nil
}

func matchingReceipt(value any, expectedHash, blockNumber, blockHash string) (map[string]any, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, failure.Evidencef("invalid_block_receipts", "eth_getBlockReceipts result is not an array")
	}
	matches := make([]any, 0, 1)
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, failure.Evidencef("invalid_block_receipts", "block receipt entry is not an object")
		}
		hash, ok := object["transactionHash"].(string)
		if !ok || evm.ValidateHash(hash) != nil {
			return nil, failure.Evidencef("invalid_block_receipts", "block receipt entry has no full transaction hash")
		}
		if strings.EqualFold(hash, expectedHash) {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		return nil, failure.Evidencef("receipt_unavailable", "eth_getBlockReceipts did not contain exactly one full transaction-hash match")
	}
	return validateReceipt(matches[0], expectedHash, blockNumber, blockHash)
}

func validateExactBlock(value any, blockNumber, blockHash string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, failure.Evidencef("block_unavailable", "exact receipt block header is unavailable")
	}
	number, ok := object["number"].(string)
	if !ok || !equalQuantity(number, blockNumber) {
		return nil, failure.Evidencef("contradictory_block", "block header number does not match the transaction")
	}
	hash, ok := object["hash"].(string)
	if !ok || evm.ValidateHash(hash) != nil || !strings.EqualFold(hash, blockHash) {
		return nil, failure.Evidencef("contradictory_block", "block header hash does not match the transaction")
	}
	return object, nil
}

func equalQuantity(left, right string) bool {
	leftValue, leftErr := evm.ParseQuantity(left)
	rightValue, rightErr := evm.ParseQuantity(right)
	return leftErr == nil && rightErr == nil && leftValue == rightValue
}

func (result ReceiptEvidence) Value() map[string]any {
	return map[string]any{
		"chain_id":         result.ChainID,
		"transaction_hash": result.TransactionHash,
		"source":           result.Source,
		"transaction":      result.Transaction,
		"receipt":          result.Receipt,
		"block":            result.Block,
	}
}
