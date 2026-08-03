// Package evidence implements RouteMesh-specific EVM evidence collection.
package evidence

import (
	"fmt"

	"github.com/paulrberg/routemesh-cli/internal/evm"
	"github.com/paulrberg/routemesh-cli/internal/strictjson"
)

const LogChunkSize uint64 = 10_000

type LogFilter struct {
	From     uint64
	To       uint64
	ToLatest bool
	base     map[string]any
}

type Chunk struct {
	From uint64 `json:"-"`
	To   uint64 `json:"-"`
}

func (c Chunk) Value() map[string]any {
	return map[string]any{"from_block": evm.Quantity(c.From), "to_block": evm.Quantity(c.To)}
}

func ParseLogFilter(data []byte) (LogFilter, error) {
	value, err := strictjson.Parse(data)
	if err != nil {
		return LogFilter{}, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return LogFilter{}, fmt.Errorf("log filter must be a JSON object")
	}
	for key := range object {
		switch key {
		case "fromBlock", "toBlock", "address", "topics":
		case "blockHash":
			return LogFilter{}, fmt.Errorf("blockHash is not accepted; use an explicit block range")
		default:
			return LogFilter{}, fmt.Errorf("unknown log filter field %q", key)
		}
	}
	fromRaw, ok := object["fromBlock"].(string)
	if !ok {
		return LogFilter{}, fmt.Errorf("fromBlock is required and must be a numeric block quantity")
	}
	from, err := evm.ParseQuantity(fromRaw)
	if err != nil {
		return LogFilter{}, fmt.Errorf("invalid fromBlock: %w", err)
	}
	toRaw, ok := object["toBlock"].(string)
	if !ok {
		return LogFilter{}, fmt.Errorf("toBlock is required and must be a numeric block quantity or latest")
	}
	filter := LogFilter{From: from, base: make(map[string]any, len(object))}
	if toRaw == "latest" {
		filter.ToLatest = true
	} else {
		filter.To, err = evm.ParseQuantity(toRaw)
		if err != nil {
			return LogFilter{}, fmt.Errorf("invalid toBlock: %w", err)
		}
		if filter.To < filter.From {
			return LogFilter{}, fmt.Errorf("toBlock must not be below fromBlock")
		}
	}
	if address, exists := object["address"]; exists {
		if err := validateAddresses(address); err != nil {
			return LogFilter{}, err
		}
	}
	if topics, exists := object["topics"]; exists {
		if err := validateTopics(topics); err != nil {
			return LogFilter{}, err
		}
	}
	for key, item := range object {
		filter.base[key] = item
	}
	filter.base["fromBlock"] = evm.Quantity(filter.From)
	if !filter.ToLatest {
		filter.base["toBlock"] = evm.Quantity(filter.To)
	}
	return filter, nil
}

func (f LogFilter) Canonical() map[string]any {
	result := make(map[string]any, len(f.base))
	for key, value := range f.base {
		result[key] = value
	}
	return result
}

func (f LogFilter) ForChunk(chunk Chunk) map[string]any {
	result := f.Canonical()
	result["fromBlock"] = evm.Quantity(chunk.From)
	result["toBlock"] = evm.Quantity(chunk.To)
	return result
}

func (f LogFilter) Chunks(upper uint64) ([]Chunk, error) {
	count, err := f.ChunkCount(upper)
	if err != nil {
		return nil, err
	}
	chunks := make([]Chunk, 0, count)
	for start := f.From; ; {
		end := upper
		if upper-start >= LogChunkSize {
			end = start + LogChunkSize - 1
		}
		chunks = append(chunks, Chunk{From: start, To: end})
		if end == upper {
			break
		}
		start = end + 1
	}
	return chunks, nil
}

func (f LogFilter) ChunkCount(upper uint64) (uint64, error) {
	if upper < f.From {
		return 0, fmt.Errorf("resolved upper block is below fromBlock")
	}
	return ((upper - f.From) / LogChunkSize) + 1, nil
}

func validateAddresses(value any) error {
	validate := func(raw any) error {
		address, ok := raw.(string)
		if !ok {
			return fmt.Errorf("address entries must be strings")
		}
		if err := evm.ValidateAddress(address); err != nil {
			return fmt.Errorf("invalid address: %w", err)
		}
		return nil
	}
	switch typed := value.(type) {
	case string:
		return validate(typed)
	case []any:
		if len(typed) == 0 {
			return fmt.Errorf("address array must not be empty")
		}
		for _, address := range typed {
			if err := validate(address); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("address must be a string or non-empty array of strings")
	}
}

func validateTopics(value any) error {
	topics, ok := value.([]any)
	if !ok {
		return fmt.Errorf("topics must be an array")
	}
	if len(topics) > 4 {
		return fmt.Errorf("topics must contain at most four positions")
	}
	for position, topic := range topics {
		switch typed := topic.(type) {
		case nil:
		case string:
			if err := evm.ValidateTopic(typed); err != nil {
				return fmt.Errorf("invalid topic at position %d: %w", position, err)
			}
		case []any:
			if len(typed) == 0 {
				return fmt.Errorf("topic alternatives at position %d must not be empty", position)
			}
			for _, alternative := range typed {
				if alternative == nil {
					continue
				}
				raw, ok := alternative.(string)
				if !ok {
					return fmt.Errorf("topic alternatives at position %d must be strings or null", position)
				}
				if err := evm.ValidateTopic(raw); err != nil {
					return fmt.Errorf("invalid topic alternative at position %d: %w", position, err)
				}
			}
		default:
			return fmt.Errorf("topic at position %d must be a hash, null, or alternatives array", position)
		}
	}
	return nil
}
