package auth

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type runnerStub struct {
	runName   string
	runArgs   []string
	runStdin  string
	runErr    error
	output    string
	outputErr error
}

func (r *runnerStub) Run(_ context.Context, name string, args []string, stdin io.Reader, _, _ io.Writer) error {
	r.runName = name
	r.runArgs = append([]string(nil), args...)
	if stdin != nil {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		r.runStdin = string(data)
	}
	return r.runErr
}

func (r *runnerStub) Output(context.Context, string, []string) ([]byte, error) {
	return []byte(r.output), r.outputErr
}

func availableStore(runner Runner) *MacStore {
	return NewMacStore(MacStoreOptions{
		GOOS:       "darwin",
		Path:       "/usr/bin/security",
		Runner:     runner,
		PathExists: func(string) bool { return true },
	})
}

func TestAddInteractiveKeepsSecretOutOfArgv(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{}
	store := availableStore(runner)
	require.NoError(t, store.AddInteractive(context.Background(), "sentinel-secret", io.Discard, io.Discard))
	assert.Equal(t, "/usr/bin/security", runner.runName)
	require.NotEmpty(t, runner.runArgs)
	assert.Equal(t, "-w", runner.runArgs[len(runner.runArgs)-1])
	assert.NotContains(t, runner.runArgs, "sentinel-secret")
	assert.Equal(t, []string{"add-generic-password", "-U", "-s", KeychainService, "-a", KeychainAccount, "-w"}, runner.runArgs)
	assert.Equal(t, "sentinel-secret\n", runner.runStdin)
}

func TestResolveCredentialPrecedence(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{output: "keychain-key\n"}
	store := availableStore(runner)
	key, source, err := Resolve(context.Background(), func(string) string { return "environment-key" }, store)
	require.NoError(t, err)
	assert.Equal(t, "environment-key", key)
	assert.Equal(t, "environment", source)

	key, source, err = Resolve(context.Background(), func(string) string { return "" }, store)
	require.NoError(t, err)
	assert.Equal(t, "keychain-key", key)
	assert.Equal(t, "keychain", source)
}

func TestResolveMissingCredential(t *testing.T) {
	t.Parallel()

	store := NewMacStore(MacStoreOptions{GOOS: "linux", PathExists: func(string) bool { return false }})
	_, _, err := Resolve(context.Background(), func(string) string { return "" }, store)
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, failure.Credential, typed.ExitCode)
}

func TestDeleteIsIdempotentForMissingItem(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{runErr: fakeExitError{}}
	store := availableStore(runner)
	deleted, err := store.Delete(context.Background())
	require.NoError(t, err)
	assert.False(t, deleted)
}

type fakeExitError struct{}

func (fakeExitError) Error() string { return "missing" }
func (fakeExitError) ExitCode() int { return 44 }

func TestUnavailableStore(t *testing.T) {
	t.Parallel()

	store := NewMacStore(MacStoreOptions{GOOS: "linux", PathExists: func(string) bool { return true }})
	assert.False(t, store.Available())
	_, err := store.Get(context.Background())
	assert.ErrorIs(t, err, ErrUnavailable)
	_, err = store.Delete(context.Background())
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestRunnerErrorDoesNotExposeOutput(t *testing.T) {
	t.Parallel()

	runner := &runnerStub{outputErr: errors.New("sentinel-secret")}
	store := availableStore(runner)
	_, _, err := Resolve(context.Background(), func(string) string { return "" }, store)
	var typed *failure.Error
	require.ErrorAs(t, err, &typed)
	assert.NotContains(t, typed.Message, "sentinel-secret")
}
