package evm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseChainIDHardening(t *testing.T) {
	t.Parallel()

	for _, valid := range []string{"1", "137", "18446744073709551615"} {
		_, err := ParseChainID(valid)
		require.NoError(t, err, valid)
	}
	for _, invalid := range []string{"", "0", "01", "+1", "-1", " 1", "1 ", "1_000", "1/../2", "1%2f2", "1?x", "1#x", "1\x00", "18446744073709551616"} {
		_, err := ParseChainID(invalid)
		require.Error(t, err, invalid)
	}
}

func TestQuantityAndHexValidation(t *testing.T) {
	t.Parallel()

	value, err := ParseQuantity("0x2a")
	require.NoError(t, err)
	assert.Equal(t, uint64(42), value)
	assert.Equal(t, "0x2a", Quantity(value))

	for _, invalid := range []string{"2a", "0x", "0x00", "0x01", "0xg", "latest"} {
		_, err := ParseQuantity(invalid)
		require.Error(t, err, invalid)
	}
	require.NoError(t, ValidateAddress("0x"+repeat("ab", 20)))
	require.NoError(t, ValidateHash("0x"+repeat("ab", 32)))
	require.Error(t, ValidateHash("0x1234"))
}

func repeat(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}
