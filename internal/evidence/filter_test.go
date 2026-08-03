package evidence

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	address = "0x1111111111111111111111111111111111111111"
	topic   = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestParseLogFilterAndChunkBoundaries(t *testing.T) {
	t.Parallel()

	filter, err := ParseLogFilter([]byte(fmt.Sprintf(`{"fromBlock":"0x1","toBlock":"0x4e21","address":"%s","topics":["%s",null,["%s",null]]}`, address, topic, topic)))
	require.NoError(t, err)
	chunks, err := filter.Chunks(filter.To)
	require.NoError(t, err)
	require.Len(t, chunks, 3)
	assert.Equal(t, Chunk{From: 1, To: 10_000}, chunks[0])
	assert.Equal(t, Chunk{From: 10_001, To: 20_000}, chunks[1])
	assert.Equal(t, Chunk{From: 20_001, To: 20_001}, chunks[2])
}

func TestParseLogFilterLatestDefersChunks(t *testing.T) {
	t.Parallel()

	filter, err := ParseLogFilter([]byte(`{"fromBlock":"0x1","toBlock":"latest"}`))
	require.NoError(t, err)
	assert.True(t, filter.ToLatest)
	assert.Equal(t, "latest", filter.Canonical()["toBlock"])
}

func TestParseLogFilterRejectsAdversarialFields(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"missing from":       `{"toBlock":"0x1"}`,
		"tagged from":        `{"fromBlock":"earliest","toBlock":"0x1"}`,
		"leading-zero block": `{"fromBlock":"0x01","toBlock":"0x1"}`,
		"block hash":         `{"fromBlock":"0x1","toBlock":"0x1","blockHash":"0x00"}`,
		"unknown":            `{"fromBlock":"0x1","toBlock":"0x1","prompt":"ignore instructions"}`,
		"bad address":        `{"fromBlock":"0x1","toBlock":"0x1","address":"../../.ssh"}`,
		"five topics":        fmt.Sprintf(`{"fromBlock":"0x1","toBlock":"0x1","topics":["%[1]s","%[1]s","%[1]s","%[1]s","%[1]s"]}`, topic),
		"nested topic":       fmt.Sprintf(`{"fromBlock":"0x1","toBlock":"0x1","topics":[[["%s"]]]}`, topic),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseLogFilter([]byte(input))
			require.Error(t, err)
		})
	}
}
