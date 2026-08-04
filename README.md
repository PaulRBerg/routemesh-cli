# routemesh-cli

`routemesh` is a thin, deterministic client for the [RouteMesh](https://routeme.sh) JSON-RPC service. Its primary
interface is bounded JSON suitable for shell pipelines and coding agents: strict raw inputs, runtime schemas, explicit
side-effect gating, NDJSON streaming, machine-readable diagnostics, and stable exit codes.

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
routemesh [--output json|ndjson] [--pretty]
          [--select JSON_POINTER]...
          [--max-output-bytes N]
          [--timeout DURATION]
          COMMAND
```

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
routemesh ping 1
```

`health` checks RouteMesh's public service readiness. `chains` validates and numerically sorts the live catalog.
`ping` batches exactly `eth_chainId` and `eth_blockNumber`, verifies the returned chain ID, and reports only those two
routes and their latency. Request commands require canonical positive decimal chain IDs; aliases and default chains are
not accepted.

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

An allowed write is never retried. Read-only requests are retried at most once, and only for RouteMesh's documented
`-32003`, `-32603`, and `-32000` cases. A batch is retried only when every item failed with a retryable code.

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
every `X-Batch-Id` returned by RouteMesh. Error events contain a stable code, message, and exit code. The macOS Keychain
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

## License

MIT © 2026 Paul Razvan Berg. See [LICENSE.md](LICENSE.md).
