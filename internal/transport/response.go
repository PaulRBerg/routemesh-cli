package transport

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/paulrberg/routemesh-cli/internal/jsonrpc"
)

func validateResponse(value any, envelope jsonrpc.Envelope) (RPCResult, error) {
	if envelope.Batch {
		items, ok := value.([]any)
		if !ok {
			return RPCResult{}, fmt.Errorf("batch request returned a non-array response")
		}
		if len(items) != len(envelope.Requests) {
			return RPCResult{}, fmt.Errorf("batch response count %d does not match request count %d", len(items), len(envelope.Requests))
		}
		codes := make([]int64, 0, len(items))
		allErrors := true
		for i, item := range items {
			code, hasError, err := validateResponseItem(item, envelope.Requests[i].ID)
			if err != nil {
				return RPCResult{}, fmt.Errorf("batch response %d: %w", i, err)
			}
			if hasError {
				codes = append(codes, code)
			} else {
				allErrors = false
			}
		}
		return RPCResult{Value: value, Batch: true, HasError: len(codes) > 0, ErrorCodes: batchRetryCodes(codes, allErrors)}, nil
	}
	code, hasError, err := validateResponseItem(value, envelope.Requests[0].ID)
	if err != nil {
		return RPCResult{}, err
	}
	codes := []int64(nil)
	if hasError {
		codes = []int64{code}
	}
	return RPCResult{Value: value, HasError: hasError, ErrorCodes: codes}, nil
}

func validateResponseItem(value any, expectedID any) (int64, bool, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return 0, false, fmt.Errorf("response must be an object")
	}
	version, ok := object["jsonrpc"].(string)
	if !ok || version != "2.0" {
		return 0, false, fmt.Errorf("jsonrpc must be exactly %q", "2.0")
	}
	id, exists := object["id"]
	if !exists || !jsonrpc.EqualID(id, expectedID) {
		return 0, false, fmt.Errorf("response id does not match request id")
	}
	_, hasResult := object["result"]
	errorValue, hasError := object["error"]
	if hasResult == hasError {
		return 0, false, fmt.Errorf("response must contain exactly one of result or error")
	}
	if !hasError {
		return 0, false, nil
	}
	errorObject, ok := errorValue.(map[string]any)
	if !ok {
		return 0, false, fmt.Errorf("error must be an object")
	}
	codeNumber, ok := errorObject["code"].(json.Number)
	if !ok {
		return 0, false, fmt.Errorf("error code must be an integer")
	}
	codeRaw := codeNumber.String()
	code, err := strconv.ParseInt(codeRaw, 10, 64)
	if err != nil || codeRaw != strconv.FormatInt(code, 10) {
		return 0, false, fmt.Errorf("error code must be an integer")
	}
	if _, ok := errorObject["message"].(string); !ok {
		return 0, false, fmt.Errorf("error message must be a string")
	}
	return code, true, nil
}

func batchRetryCodes(codes []int64, allErrors bool) []int64 {
	if !allErrors {
		return nil
	}
	return codes
}
