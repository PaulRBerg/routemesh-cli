package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/paulrberg/routemesh-cli/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type doerStub struct {
	calls int
	do    func(*http.Request) (*http.Response, error)
}

func (d *doerStub) Do(request *http.Request) (*http.Response, error) {
	d.calls++
	if d.do == nil {
		return nil, errors.New("unexpected HTTP request")
	}
	return d.do(request)
}

type keychainStub struct {
	available bool
	platform  string
	key       string
	getErr    error
	gets      int
	adds      int
	deletes   int
}

func (s *keychainStub) Available() bool { return s.available }
func (s *keychainStub) Platform() string {
	if s.platform == "" {
		return "darwin"
	}
	return s.platform
}
func (*keychainStub) ToolPath() string { return auth.SecurityPath }
func (s *keychainStub) Get(context.Context) (string, error) {
	s.gets++
	return s.key, s.getErr
}
func (s *keychainStub) AddInteractive(context.Context, io.Reader, io.Writer, io.Writer) error {
	s.adds++
	return nil
}
func (s *keychainStub) Delete(context.Context) (bool, error) {
	s.deletes++
	return true, nil
}

type execution struct {
	code   int
	stdout string
	stderr string
}

func execute(t *testing.T, args []string, dependencies Dependencies) execution {
	t.Helper()
	var stdout, stderr bytes.Buffer
	dependencies.Stdout = &stdout
	dependencies.Stderr = &stderr
	if dependencies.Stdin == nil {
		dependencies.Stdin = strings.NewReader("")
	}
	if dependencies.Getenv == nil {
		dependencies.Getenv = func(string) string { return "" }
	}
	result := Execute(context.Background(), args, dependencies)
	return execution{code: result, stdout: stdout.String(), stderr: stderr.String()}
}

func httpResponse(status int, body string, headers map[string]string) *http.Response {
	header := make(http.Header)
	for key, value := range headers {
		header.Set(key, value)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func decodeObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &value))
	return value
}

func decodeLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	values := make([]map[string]any, len(lines))
	for i, line := range lines {
		require.NoError(t, json.Unmarshal([]byte(line), &values[i]))
	}
	return values
}

func TestSchemaIndexAndDetail(t *testing.T) {
	t.Parallel()

	index := execute(t, []string{"schema"}, Dependencies{})
	assert.Equal(t, 0, index.code)
	assert.Empty(t, index.stderr)
	assert.Len(t, decodeObject(t, index.stdout)["commands"], 11)

	detail := execute(t, []string{"schema", "auth", "status"}, Dependencies{})
	assert.Equal(t, 0, detail.code)
	assert.Equal(t, "auth status", decodeObject(t, detail.stdout)["x-routemesh-command"])
}

func TestRPCDryRunDoesNoHTTPOrCredentialAccess(t *testing.T) {
	t.Parallel()

	doer := &doerStub{}
	keychain := &keychainStub{available: true, key: "sentinel-key"}
	result := execute(t, []string{"rpc", "1", "--json", "-", "--dry-run"}, Dependencies{
		Stdin:      strings.NewReader(`{"jsonrpc":"2.0","method":"eth_chainId","id":"agent"}`),
		HTTPClient: doer,
		Keychain:   keychain,
	})
	assert.Equal(t, 0, result.code)
	assert.Equal(t, 0, doer.calls)
	assert.Equal(t, 0, keychain.gets)
	assert.NotContains(t, result.stdout, "sentinel-key")
	value := decodeObject(t, result.stdout)
	assert.Equal(t, "https://lb.routeme.sh/rpc/1/<redacted>", value["destination"])
}

func TestWriteGatingAndDryRunRetryContract(t *testing.T) {
	t.Parallel()

	blocked := execute(t, []string{"rpc", "1", "eth_sendRawTransaction", "--params", `[]`, "--dry-run"}, Dependencies{})
	assert.Equal(t, 2, blocked.code)
	assert.Empty(t, blocked.stdout)
	assert.Equal(t, "write_not_allowed", decodeLines(t, blocked.stderr)[0]["code"])

	allowed := execute(t, []string{"rpc", "1", "eth_sendRawTransaction", "--params", `[]`, "--allow-write", "--dry-run"}, Dependencies{})
	assert.Equal(t, 0, allowed.code)
	value := decodeObject(t, allowed.stdout)
	assert.Equal(t, "external_write", value["side_effect"])
	retry := value["retry"].(map[string]any)
	assert.Equal(t, false, retry["eligible"])
	assert.Equal(t, float64(1), retry["max_attempts"])
}

func TestRPCProviderErrorEmitsCompleteResponseAndExitFive(t *testing.T) {
	t.Parallel()

	doer := &doerStub{do: func(*http.Request) (*http.Response, error) {
		return httpResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"unsupported\u001b[31m"}}`, map[string]string{"X-Batch-Id": "batch-final"}), nil
	}}
	result := execute(t, []string{"rpc", "1", "eth_chainId"}, Dependencies{
		HTTPClient: doer,
		Getenv: func(name string) string {
			if name == auth.EnvironmentVariable {
				return "sentinel-key"
			}
			return ""
		},
	})
	assert.Equal(t, 5, result.code)
	assert.NotContains(t, result.stdout, string(rune(0x1b)))
	assert.Equal(t, float64(-32001), decodeObject(t, result.stdout)["error"].(map[string]any)["code"])
	events := decodeLines(t, result.stderr)
	require.Len(t, events, 2)
	assert.Equal(t, "batch-final", events[0]["batch_id"])
	assert.Equal(t, float64(5), events[1]["exit_code"])
	assert.NotContains(t, result.stderr, "sentinel-key")
}

func TestRPCBatchNDJSON(t *testing.T) {
	t.Parallel()

	doer := &doerStub{do: func(*http.Request) (*http.Response, error) {
		return httpResponse(http.StatusOK, `[{"jsonrpc":"2.0","id":1,"result":"0x1"},{"jsonrpc":"2.0","id":2,"result":"0x2"}]`, nil), nil
	}}
	raw := `[{"jsonrpc":"2.0","method":"eth_chainId","id":1},{"jsonrpc":"2.0","method":"eth_blockNumber","id":2}]`
	result := execute(t, []string{"--output", "ndjson", "rpc", "1", "--json", raw}, Dependencies{
		HTTPClient: doer,
		Getenv:     func(string) string { return "key" },
	})
	assert.Equal(t, 0, result.code)
	assert.Len(t, decodeLines(t, result.stdout), 2)
	assert.Len(t, decodeLines(t, result.stderr), 1)
}

func TestOutputValidationPrecedesNetworkAndNeverWritesPartialOutput(t *testing.T) {
	t.Parallel()

	doer := &doerStub{}
	invalid := execute(t, []string{"--output", "ndjson", "--pretty", "health"}, Dependencies{HTTPClient: doer})
	assert.Equal(t, 2, invalid.code)
	assert.Equal(t, 0, doer.calls)
	assert.Empty(t, invalid.stdout)

	missing := execute(t, []string{"--select", "/missing", "schema"}, Dependencies{})
	assert.Equal(t, 2, missing.code)
	assert.Empty(t, missing.stdout)
	assert.Contains(t, missing.stderr, "missing")

	limited := execute(t, []string{"--max-output-bytes", "8", "schema"}, Dependencies{})
	assert.Equal(t, 2, limited.code)
	assert.Empty(t, limited.stdout)
}

func TestAuthStatusAndDryRunsNeverExposeOrMutateCredentials(t *testing.T) {
	t.Parallel()

	keychain := &keychainStub{available: true, key: "sentinel-key"}
	status := execute(t, []string{"auth", "status"}, Dependencies{
		Keychain: keychain,
		Getenv:   func(string) string { return "environment-sentinel" },
	})
	assert.Equal(t, 0, status.code)
	value := decodeObject(t, status.stdout)
	assert.Len(t, value, 3)
	assert.Equal(t, "configured", value["environment"])
	assert.Equal(t, "environment", value["active_source"])
	assert.NotContains(t, status.stdout, "sentinel")

	clear := execute(t, []string{"auth", "clear", "--dry-run"}, Dependencies{Keychain: keychain})
	assert.Equal(t, 0, clear.code)
	assert.Equal(t, 0, keychain.deletes)

	nonMac := &keychainStub{available: false, platform: "linux"}
	initResult := execute(t, []string{"init", "1", "--dry-run"}, Dependencies{Keychain: nonMac})
	assert.Equal(t, 3, initResult.code)
	assert.Equal(t, 0, nonMac.adds)
	assert.NotEmpty(t, initResult.stdout)
}

func TestInitValidatesTheStoredKeyIgnoringEnvironmentOverride(t *testing.T) {
	t.Parallel()

	keychain := &keychainStub{available: true, key: "keychain-sentinel"}
	var requestedURL string
	doer := &doerStub{do: func(request *http.Request) (*http.Response, error) {
		requestedURL = request.URL.String()
		return httpResponse(http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`, nil), nil
	}}
	result := execute(t, []string{"init", "1"}, Dependencies{
		HTTPClient: doer,
		Keychain:   keychain,
		Getenv:     func(string) string { return "environment-sentinel" },
	})
	assert.Equal(t, 0, result.code)
	assert.Equal(t, 1, keychain.adds)
	assert.Equal(t, 1, keychain.gets)
	assert.Contains(t, requestedURL, "keychain-sentinel")
	assert.NotContains(t, requestedURL, "environment-sentinel")
	assert.NotContains(t, result.stdout+result.stderr, "sentinel")
}

func TestSchemaAPIFetch(t *testing.T) {
	t.Parallel()

	doer := &doerStub{do: func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "/openapi.json", request.URL.Path)
		return httpResponse(http.StatusOK, `{"openapi":"3.0.0","paths":{}}`, nil), nil
	}}
	result := execute(t, []string{"schema", "api"}, Dependencies{
		HTTPClient: doer,
		OpenAPIURL: "https://docs.example.test/openapi.json",
	})
	assert.Equal(t, 0, result.code)
	assert.Equal(t, "3.0.0", decodeObject(t, result.stdout)["openapi"])
}

func TestHealthAndChainsValidateLiveShapes(t *testing.T) {
	t.Parallel()

	doer := &doerStub{do: func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/health":
			return httpResponse(http.StatusOK, `{"success":true,"message":"ready"}`, nil), nil
		case "/chains":
			return httpResponse(http.StatusOK, `[{"chain_id":"10","name":"Ten"},{"chain_id":"1","name":"One"}]`, nil), nil
		default:
			return nil, errors.New("unexpected path")
		}
	}}
	health := execute(t, []string{"health"}, Dependencies{HTTPClient: doer})
	assert.Equal(t, 0, health.code)
	assert.Equal(t, true, decodeObject(t, health.stdout)["ready"])

	chains := execute(t, []string{"--output", "ndjson", "chains"}, Dependencies{HTTPClient: doer})
	assert.Equal(t, 0, chains.code)
	lines := decodeLines(t, chains.stdout)
	assert.Equal(t, "1", lines[0]["chain_id"])
	assert.Equal(t, "10", lines[1]["chain_id"])
}

func TestTransportAndEvidenceExitCodes(t *testing.T) {
	t.Parallel()

	transportFailure := execute(t, []string{"rpc", "1", "eth_chainId"}, Dependencies{
		HTTPClient: &doerStub{do: func(*http.Request) (*http.Response, error) { return nil, errors.New("down") }},
		Getenv:     func(string) string { return "key" },
	})
	assert.Equal(t, 4, transportFailure.code)
	assert.Empty(t, transportFailure.stdout)

	chainMismatch := execute(t, []string{"ping", "1"}, Dependencies{
		HTTPClient: &doerStub{do: func(*http.Request) (*http.Response, error) {
			return httpResponse(http.StatusOK, `[{"jsonrpc":"2.0","id":1,"result":"0x2"},{"jsonrpc":"2.0","id":2,"result":"0x10"}]`, nil), nil
		}},
		Getenv: func(string) string { return "key" },
	})
	assert.Equal(t, 6, chainMismatch.code)
	assert.Empty(t, chainMismatch.stdout)
}

func TestLogsAndReceiptDryRunsAreNetworkFree(t *testing.T) {
	t.Parallel()

	doer := &doerStub{}
	logs := execute(t, []string{"logs", "1", "--json", `{"fromBlock":"0x1","toBlock":"latest"}`, "--dry-run"}, Dependencies{HTTPClient: doer})
	assert.Equal(t, 0, logs.code)
	assert.Equal(t, 0, doer.calls)
	assert.Nil(t, decodeObject(t, logs.stdout)["chunks"])

	hash := "0x" + strings.Repeat("a", 64)
	receipt := execute(t, []string{"receipt", "1", hash, "--dry-run"}, Dependencies{HTTPClient: doer})
	assert.Equal(t, 0, receipt.code)
	assert.Equal(t, 0, doer.calls)
	assert.Len(t, decodeObject(t, receipt.stdout)["requests"], 4)
}

func TestOutputEnvironmentDefaults(t *testing.T) {
	t.Setenv("ROUTEMESH_OUTPUT", "ndjson")
	t.Setenv("ROUTEMESH_MAX_OUTPUT_BYTES", "1048576")

	doer := &doerStub{do: func(*http.Request) (*http.Response, error) {
		return httpResponse(http.StatusOK, `[{"chain_id":"1","name":"One"},{"chain_id":"10","name":"Ten"}]`, nil), nil
	}}
	result := execute(t, []string{"chains"}, Dependencies{HTTPClient: doer})
	assert.Equal(t, 0, result.code)
	assert.Len(t, decodeLines(t, result.stdout), 2)
}
