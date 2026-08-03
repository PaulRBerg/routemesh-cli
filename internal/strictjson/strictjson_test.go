package strictjson

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRejectsAmbiguousJSON(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"duplicate key":  `{"a":1,"a":2}`,
		"trailing value": `{} []`,
		"unterminated":   `{"a":`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(input))
			require.Error(t, err)
		})
	}
}

func TestParseRejectsExcessiveDepth(t *testing.T) {
	t.Parallel()

	input := strings.Repeat("[", MaxDepth+1) + strings.Repeat("]", MaxDepth+1)
	_, err := Parse([]byte(input))
	require.ErrorContains(t, err, "nesting")
}

func TestReadRejectsExcessiveSize(t *testing.T) {
	t.Parallel()

	_, err := Read(strings.NewReader("12345"), 4)
	require.ErrorContains(t, err, "exceeds")
}
