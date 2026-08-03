default:
    @just --list

# Build the routemesh binary.
@build:
    mkdir -p bin
    go build -o bin/routemesh ./cmd/routemesh

# Run all Go tests.
@test:
    go test ./...

# Run the strict JSON-RPC fuzz target for a bounded interval.
@fuzz:
    go test ./internal/jsonrpc -run '^$' -fuzz '^FuzzParseRaw$' -fuzztime 10s

# Run static analysis and verify formatting.
@lint:
    golangci-lint run ./...
    golangci-lint fmt --diff ./...

# Format Go source.
@format:
    golangci-lint fmt ./...

# Run the complete local acceptance suite.
@check:
    just build
    just test
    just lint

# Exercise public discovery and authenticated EVM routes without printing credentials.
@smoke chain_id:
    just build
    ./bin/routemesh auth status | jq -e '.active_source != "none"' >/dev/null
    ./bin/routemesh health
    ./bin/routemesh --select '/0' chains
    ./bin/routemesh ping {{ quote(chain_id) }}
