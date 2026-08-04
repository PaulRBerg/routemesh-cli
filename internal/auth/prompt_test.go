package auth

import (
	"io"
	"strings"
	"testing"

	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadAPIKeyInteractiveMatching(t *testing.T) {
	t.Parallel()

	key, err := ReadAPIKeyInteractive(strings.NewReader("secret\nsecret\n"), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "secret", key)
}

func TestReadAPIKeyInteractiveMismatch(t *testing.T) {
	t.Parallel()

	_, err := ReadAPIKeyInteractive(strings.NewReader("first-secret\nsecond-secret\n"), io.Discard)
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "api_key_mismatch", typed.Kind)
	assert.NotContains(t, typed.Message, "first-secret")
	assert.NotContains(t, typed.Message, "second-secret")
}

func TestReadAPIKeyInteractiveEmpty(t *testing.T) {
	t.Parallel()

	_, err := ReadAPIKeyInteractive(strings.NewReader("\nsecret\n"), io.Discard)
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "api_key_empty", typed.Kind)
}

func TestReadAPIKeyInteractiveTruncated(t *testing.T) {
	t.Parallel()

	_, err := ReadAPIKeyInteractive(strings.NewReader("secret\n"), io.Discard)
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "prompt_read_error", typed.Kind)
}
