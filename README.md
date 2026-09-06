# routemesh-cli

`routemesh` is a thin, deterministic client for the [RouteMesh](https://routeme.sh) JSON-RPC service. Its primary
interface is bounded JSON suitable for shell pipelines and coding agents: strict raw inputs, runtime schemas, explicit
side-effect gating, NDJSON output, machine-readable diagnostics, and stable exit codes.

It does not interpret transactions, discover history through explorers, sign payloads, or claim that one successful
route proves general provider or archive health.

This is an unofficial, community-built project and is not affiliated with or endorsed by RouteMesh.

## Install

Go 1.26 or newer is required.

```sh
go install github.com/paulrberg/routemesh-cli/cmd/routemesh@latest
```

To build from a checkout:

```sh
just build
./bin/routemesh schema
```

## Authentication

Ordinary requests use the first non-empty source in this order:

1. `ROUTEMESH_API_KEY`
2. the macOS Keychain item with service `routemesh-cli` and account `ROUTEMESH_API_KEY`

There is deliberately no API-key flag, credential file, or credentialized URL output.

On macOS, initialize Keychain. Keychain itself prompts for the secret; the secret is not put in process arguments. The
stored key is then retrieved and probed with `eth_chainId`, ignoring any environment override for that validation.
Validation defaults to chain ID 1 (Ethereum mainnet); pass an explicit chain ID to validate against a different chain.

```sh
routemesh init --dry-run
routemesh init
routemesh init 10 --dry-run
routemesh auth status
routemesh auth clear --dry-run
routemesh auth clear
```

`auth status` emits only `environment`, `keychain`, and `active_source` states. `auth clear` is idempotent and never
changes the environment. Non-macOS systems can use `ROUTEMESH_API_KEY`; Keychain initialization is unavailable there.

## Interface

```text
routemesh --version
routemesh [--output json|ndjson] [--pretty]
          [--select JSON_POINTER]...
          [--max-output-bytes N]
          [--timeout DURATION]
          COMMAND
```

Every build reports the fixed version `1.0.0`; this project does not maintain a version progression.

Output defaults to compact JSON with a 1 MiB encoded limit. `ROUTEMESH_OUTPUT` and
`ROUTEMESH_MAX_OUTPUT_BYTES` set the corresponding defaults. `--pretty` applies only to JSON.

`--select` evaluates an RFC 6901 JSON Pointer before the output-size check. One pointer emits its value. Repeated
pointers emit an ordered array of `{ "pointer", "value" }` records. A missing pointer is an error and stdout remains
empty.

```sh
routemesh chains --select '/0/name'
routemesh ping 1 --select '/block_number' --select '/latency_ms'
routemesh --max-output-bytes 4096 schema rpc
```

NDJSON records are independently parseable: one chain per line, one response per batch item, and typed `checkpoint`,
`log`, and `summary` records for log sweeps.

```sh
routemesh --output ndjson chains
routemesh --output ndjson rpc 1 --json '[
  {"jsonrpc":"2.0","method":"eth_chainId","id":1},
  {"jsonrpc":"2.0","method":"eth_blockNumber","id":2}
]'
```

Stdout is buffered until validation and size checks finish. Provider strings are untrusted data: responses are decoded
and re-encoded as valid UTF-8 JSON, terminal controls are escaped, no ANSI is emitted, and no semantic
prompt-injection rewriting is attempted.

## Runtime schemas

Use schema discovery instead of scraping help text:

```sh
routemesh schema
routemesh schema rpc
routemesh schema logs
routemesh schema subscribe
routemesh schema auth status
routemesh schema api
```

The index describes accepted input modes, output formats, and side effects. Command details are bundled JSON Schema
Draft 2020-12 documents covering inputs, outputs, stderr events, dry-run plans, and exit codes. Provider-returned fields
are annotated as untrusted.

`schema api` fetches RouteMesh's current official OpenAPI document. That document intentionally leaves JSON-RPC
`params` and `result` method-dependent and opaque; this CLI does not claim method-level schemas that RouteMesh does not
publish.

## Discovery and route checks

```sh
routemesh health
routemesh chains
routemesh chains --transport ws
routemesh ping 1
```

`health` checks RouteMesh's public service readiness. `chains` validates and numerically sorts the live HTTP RPC
catalog from `GET /chains/rpc`. `chains --transport ws` selects the separate WebSocket catalog at `GET /chains/ws`.
The default is `--transport rpc`; neither command uses the deprecated `GET /chains` endpoint.
`ping` batches exactly `eth_chainId` and `eth_blockNumber`, verifies the returned chain ID, and reports only those two
routes and their latency. Request commands require canonical positive decimal chain IDs; aliases and default chains are
not accepted.

## WebSocket subscriptions

Use `subscribe` to wait for live activity without polling. It uses the same credential source as HTTP RPC, with
`wss://` replacing `https://` in the RouteMesh URL.

```sh
routemesh subscribe 1 newHeads --dry-run
routemesh --timeout 60s subscribe 1 newHeads --count 2
routemesh --output ndjson subscribe 1 newPendingTransactions --count 5
routemesh --timeout 60s subscribe 1 logs --json '{
  "address": "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"
}'
```

Check `chains --transport ws` for chain coverage. Subscription-type availability is provider-dependent; RouteMesh
publishes the current types in its [WebSocket pricing catalog](https://api.routeme.sh/pricing/ws).

`--count` defaults to 1 and accepts 1–1000 notifications. The global `--timeout` defaults to 30 seconds. Output is
buffered until the requested count is reached: JSON is an array of `eth_subscription` envelopes, and NDJSON emits one
envelope per line. `--select` and `--max-output-bytes` apply as usual. A timeout, disconnect, malformed message, or
output-limit failure emits no partial stdout. The connection closes when collection finishes; the CLI never reconnects
or retries a subscription. Received JSON is limited to 32 MiB in total, including the subscription acknowledgement.

Log filters accept only `address` and `topics`; `--json -` reads the filter from stdin. The CLI validates notification
envelopes, subscription IDs, block/header fields, transaction hashes, and log fields against the requested filter.
Live notifications are observations, not proof of finality or historical completeness. Reorg notifications, including
logs with `removed: true`, are preserved in arrival order and count toward `--count`. Use `logs` with an explicit range
or `receipt` to verify subsequent canonical evidence. A fresh subscription does not recover events missed while
disconnected.

## JSON-RPC

Generated mode uses request ID `1` and defaults `params` to `[]`:

```sh
routemesh rpc 1 eth_getBalance \
  --params '["0x0000000000000000000000000000000000000000","latest"]'
```

Raw mode accepts one complete JSON-RPC object or a non-empty batch. Caller IDs are preserved. Use `--json -` for a
bounded stdin payload:

```sh
printf '%s\n' '{"jsonrpc":"2.0","method":"eth_chainId","id":"agent-1"}' |
  routemesh rpc 1 --json -
```

Raw inputs are limited to 1 MiB, 64 nesting levels, and 100 batch items. Duplicate object keys, trailing documents,
duplicate batch IDs, notifications, unknown envelope fields, invalid versions, non-string/non-integer IDs, unsafe
method characters, and scalar `params` are rejected before credential access or network I/O. Application strings inside
valid array/object `params` remain opaque and may contain legitimate control characters.

Known signing, transaction submission, subscription/filter mutation, mining, engine/admin, and development-node
mutation methods require `--allow-write`:

```sh
routemesh rpc 1 eth_sendRawTransaction \
  --params '["0x..."]' \
  --allow-write \
  --dry-run
```

An allowed write is never retried. Read-only requests get at most three total attempts for RouteMesh's documented
`-32003`, `-32603`, and `-32000` JSON-RPC errors, or for HTTP 429, 502, 503, and 504 responses that contain no usable
JSON-RPC evidence. A batch is retried for JSON-RPC errors only when every item failed with a retryable code. Valid
`Retry-After` delays are honored up to 30 seconds; otherwise retries use bounded exponential backoff with full jitter.

The complete final JSON-RPC response is emitted even when it contains an error; the process then exits `5`.

## Evidence helpers

### Logs

```sh
routemesh logs 1 --json '{
  "fromBlock":"0x1200000",
  "toBlock":"latest",
  "address":"0x0000000000000000000000000000000000000000",
  "topics":[]
}' --dry-run

printf '%s\n' '{"fromBlock":"0x1200000","toBlock":"0x1202710"}' |
  routemesh --output ndjson logs 1 --json -
```

`logs` accepts only standard range-filter fields, requires numeric `fromBlock`, permits numeric or `latest` `toBlock`,
and rejects `blockHash` and unknown fields. It validates address/topic widths, resolves `latest` once, and splits the
inclusive range into at most 10,000 blocks per `eth_getLogs` request, matching RouteMesh's documented request limit.

The upper-bound block hash is fetched before and after all chunks. No log—including an empty result—is emitted unless
every chunk succeeds, entries validate and remain ordered, and the upper-bound hash is unchanged.

### Receipt

```sh
routemesh receipt 1 \
  0x0000000000000000000000000000000000000000000000000000000000000000 \
  --dry-run
```

`receipt` verifies the full transaction hash, mined transaction block references, receipt fields, and exact block
header. If the direct receipt is `null` but the transaction proves an exact block, it conditionally calls
`eth_getBlockReceipts`, requires exactly one full-hash match, and verifies that receipt against the same block. It never
falls back to explorers or public RPC endpoints.

## Dry runs and diagnostics

`rpc`, `logs`, `receipt`, `init`, and `auth clear` support `--dry-run`. Dry runs perform their local parsing,
normalization, hardening, write classification, and plan validation without RouteMesh calls or Keychain mutation.
Destinations always end in `/<redacted>`.

Stderr consists of NDJSON events. RPC attempt events contain the redacted destination, HTTP status, attempt number, and
every `X-Batch-Id` returned by RouteMesh. WebSocket handshake events include the redacted destination, HTTP status,
and `X-WebSocket-Session-ID` when available. Error events contain a stable code, message, and exit code. The macOS Keychain
prompt is the only interactive exception.

| Exit | Meaning |
| ---: | --- |
| `0` | Success or valid dry run |
| `2` | Usage, validation, schema, or output-limit failure |
| `3` | Credential or Keychain failure |
| `4` | HTTP or transport failure |
| `5` | Final JSON-RPC/provider error |
| `6` | Incomplete, unavailable, or contradictory evidence |

## Development

```sh
just build
just test
just fuzz
just lint
just format
just check
just smoke 1
```

`just smoke CHAIN_ID` requires an active credential source and exercises health, chain discovery, and the exact ping
routes without printing the credential.

Contributor workflow and implementation constraints are documented in [AGENTS.md](AGENTS.md).

## License

MIT © 2026 Paul Razvan Berg. See [LICENSE.md](LICENSE.md).
