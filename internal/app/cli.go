// Package app defines the routemesh command tree and injected runtime.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/alecthomas/kong"

	"github.com/paulrberg/routemesh-cli/internal/auth"
	"github.com/paulrberg/routemesh-cli/internal/failure"
	"github.com/paulrberg/routemesh-cli/internal/output"
	"github.com/paulrberg/routemesh-cli/internal/transport"
)

type CLI struct {
	Output         string        `name:"output" enum:"json,ndjson" default:"json" env:"ROUTEMESH_OUTPUT" help:"Output format: json or ndjson."`
	Pretty         bool          `name:"pretty" help:"Indent JSON output (not valid with NDJSON)."`
	Select         []string      `name:"select" placeholder:"JSON_POINTER" help:"Apply an RFC 6901 pointer before the output limit; repeat for multiple values."`
	MaxOutputBytes int64         `name:"max-output-bytes" default:"1048576" env:"ROUTEMESH_MAX_OUTPUT_BYTES" help:"Maximum encoded stdout bytes."`
	Timeout        time.Duration `name:"timeout" default:"30s" help:"Overall command timeout."`

	Schema  SchemaCmd  `cmd:"" help:"Inspect bundled CLI schemas or RouteMesh OpenAPI."`
	Init    InitCmd    `cmd:"" help:"Store and validate a RouteMesh API key in macOS Keychain."`
	Auth    AuthCmd    `cmd:"" help:"Inspect or clear stored credentials."`
	Health  HealthCmd  `cmd:"" help:"Check public RouteMesh service readiness."`
	Chains  ChainsCmd  `cmd:"" help:"List the live RouteMesh chain catalog."`
	Ping    PingCmd    `cmd:"" help:"Verify eth_chainId and eth_blockNumber for an EVM chain."`
	RPC     RPCCmd     `cmd:"" name:"rpc" help:"Send strict JSON-RPC requests."`
	Logs    LogsCmd    `cmd:"" help:"Collect canonical eth_getLogs evidence."`
	Receipt ReceiptCmd `cmd:"" help:"Collect and cross-check transaction receipt evidence."`
}

type SchemaCmd struct {
	Command []string `arg:"" optional:"" help:"Command path, or api for RouteMesh OpenAPI."`
}

type InitCmd struct {
	ChainID string `arg:"" name:"chain-id" help:"Canonical positive decimal chain ID."`
	DryRun  bool   `name:"dry-run" help:"Validate and emit the Keychain/probe plan without side effects."`
}

type AuthCmd struct {
	Status AuthStatusCmd `cmd:"" help:"Report credential source states."`
	Clear  AuthClearCmd  `cmd:"" help:"Remove only the RouteMesh Keychain item."`
}

type AuthStatusCmd struct{}

type AuthClearCmd struct {
	DryRun bool `name:"dry-run" help:"Emit the delete plan without modifying Keychain."`
}

type HealthCmd struct{}
type ChainsCmd struct{}

type PingCmd struct {
	ChainID string `arg:"" name:"chain-id" help:"Canonical positive decimal chain ID."`
}

type RPCCmd struct {
	ChainID    string `arg:"" name:"chain-id" help:"Canonical positive decimal chain ID."`
	Method     string `arg:"" optional:"" help:"JSON-RPC method for generated mode."`
	Params     string `name:"params" placeholder:"JSON" help:"Complete params array or object (default: [])."`
	JSON       string `name:"json" placeholder:"JSON|-" help:"Complete request object or non-empty batch; use - for stdin."`
	AllowWrite bool   `name:"allow-write" help:"Allow known signing or mutation methods."`
	DryRun     bool   `name:"dry-run" help:"Validate and emit the canonical request without network access."`
}

type LogsCmd struct {
	ChainID string `arg:"" name:"chain-id" help:"Canonical positive decimal chain ID."`
	JSON    string `name:"json" required:"" placeholder:"FILTER|-" help:"Complete eth_getLogs filter; use - for stdin."`
	DryRun  bool   `name:"dry-run" help:"Validate and emit the deterministic chunk plan without network access."`
}

type ReceiptCmd struct {
	ChainID string `arg:"" name:"chain-id" help:"Canonical positive decimal chain ID."`
	TxHash  string `arg:"" name:"tx-hash" help:"Full 32-byte transaction hash."`
	DryRun  bool   `name:"dry-run" help:"Validate and emit the conditional request plan without network access."`
}

type Dependencies struct {
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Getenv     func(string) string
	HTTPClient transport.Doer
	Keychain   auth.Store
	Sleep      transport.Sleep
	Now        func() time.Time
	APIBase    string
	RPCBase    string
	OpenAPIURL string
}

type Runtime struct {
	ctx        context.Context
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	getenv     func(string) string
	httpClient transport.Doer
	keychain   auth.Store
	sleep      transport.Sleep
	now        func() time.Time
	apiBase    string
	rpcBase    string
	openAPIURL string
	output     output.Config
}

func Execute(ctx context.Context, args []string, dependencies Dependencies) int {
	dependencies = withDefaults(dependencies)
	cli := &CLI{}
	parser, err := kong.New(
		cli,
		kong.Name("routemesh"),
		kong.Description("A thin, deterministic RouteMesh client for scripts and coding agents."),
		kong.Writers(dependencies.Stdout, dependencies.Stderr),
		kong.UsageOnError(),
	)
	if err != nil {
		emitError(dependencies.Stderr, failure.Wrap(failure.Validation, "schema_error", "initialize command schema", err))
		return failure.Validation
	}
	parsed, err := parser.Parse(args)
	if err != nil {
		emitError(dependencies.Stderr, failure.Wrap(failure.Validation, "usage_error", cleanMessage(err.Error()), err))
		return failure.Validation
	}
	if cli.Timeout <= 0 {
		emitError(dependencies.Stderr, failure.New(failure.Validation, "invalid_timeout", "timeout must be positive"))
		return failure.Validation
	}
	outputConfig := output.Config{
		Format:   cli.Output,
		Pretty:   cli.Pretty,
		Pointers: cli.Select,
		MaxBytes: cli.MaxOutputBytes,
	}
	if err := output.Validate(outputConfig); err != nil {
		emitError(dependencies.Stderr, failure.Wrap(failure.Validation, "output_error", err.Error(), err))
		return failure.Validation
	}
	commandCtx, cancel := context.WithTimeout(ctx, cli.Timeout)
	defer cancel()
	runtime := &Runtime{
		ctx:        commandCtx,
		stdin:      dependencies.Stdin,
		stdout:     dependencies.Stdout,
		stderr:     dependencies.Stderr,
		getenv:     dependencies.Getenv,
		httpClient: dependencies.HTTPClient,
		keychain:   dependencies.Keychain,
		sleep:      dependencies.Sleep,
		now:        dependencies.Now,
		apiBase:    dependencies.APIBase,
		rpcBase:    dependencies.RPCBase,
		openAPIURL: dependencies.OpenAPIURL,
		output:     outputConfig,
	}
	if err := parsed.Run(runtime); err != nil {
		typed := failure.Normalize(err)
		emitError(dependencies.Stderr, typed)
		return typed.ExitCode
	}
	return failure.Success
}

func withDefaults(dependencies Dependencies) Dependencies {
	if dependencies.Stdin == nil {
		dependencies.Stdin = os.Stdin
	}
	if dependencies.Stdout == nil {
		dependencies.Stdout = os.Stdout
	}
	if dependencies.Stderr == nil {
		dependencies.Stderr = os.Stderr
	}
	if dependencies.Getenv == nil {
		dependencies.Getenv = os.Getenv
	}
	if dependencies.HTTPClient == nil {
		dependencies.HTTPClient = &http.Client{}
	}
	if dependencies.Keychain == nil {
		dependencies.Keychain = auth.NewMacStore(auth.MacStoreOptions{})
	}
	return dependencies
}

func (r *Runtime) emit(document output.Document) error {
	if err := output.Write(r.stdout, r.output, document); err != nil {
		return failure.Wrap(failure.Validation, "output_error", cleanMessage(err.Error()), err)
	}
	return nil
}

func (r *Runtime) diagnostic(event any) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(r.stderr, "%s\n", encoded)
}

func (r *Runtime) publicClient() *transport.Client {
	return r.client("")
}

func (r *Runtime) authenticatedClient(key string) *transport.Client {
	return r.client(key)
}

func (r *Runtime) client(key string) *transport.Client {
	return transport.New(transport.Options{
		HTTPClient: r.httpClient,
		APIBase:    r.apiBase,
		RPCBase:    r.rpcBase,
		OpenAPIURL: r.openAPIURL,
		APIKey:     key,
		Diagnostic: r.diagnostic,
		Sleep:      r.sleep,
		Now:        r.now,
	})
}

func (r *Runtime) credential() (string, string, error) {
	return auth.Resolve(r.ctx, r.getenv, r.keychain)
}

func emitError(writer io.Writer, err *failure.Error) {
	event := map[string]any{
		"type":      "error",
		"code":      err.Kind,
		"message":   cleanMessage(err.Message),
		"exit_code": err.ExitCode,
	}
	if err.Details != nil {
		event["details"] = err.Details
	}
	encoded, marshalErr := json.Marshal(event)
	if marshalErr != nil {
		return
	}
	_, _ = fmt.Fprintf(writer, "%s\n", encoded)
}

func cleanMessage(message string) string {
	return string([]rune(message))
}
