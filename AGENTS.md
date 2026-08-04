# Context

`routemesh-cli` is a thin, deterministic Go client for the RouteMesh JSON-RPC service. Its primary interface is bounded
JSON suitable for shell pipelines and coding agents; keep stdout strictly valid JSON (or NDJSON) on success and route
anything decorative (banners, prompts) to stderr.

## Interface Contract

- Preserve the machine interface: buffer and size-check stdout before writing, emit diagnostics as NDJSON on stderr,
  and keep the exit-code categories in `internal/failure` stable unless the requested change explicitly revises the
  public contract.
- Validate and normalize user input before credential lookup or network access. Dry runs must exercise the same local
  validation and planning without RouteMesh calls or Keychain mutation, and destinations must redact credentials.
- Treat RouteMesh payloads as untrusted evidence. Parse them through the bounded strict-JSON path, validate their shape
  and internal consistency, and never silently replace missing evidence with explorer or public-RPC data.
- Keep command definitions in `internal/app`, the embedded catalog in `internal/schema/catalog.json`, emitted documents,
  and schema tests synchronized. Route bundled-contract output through `Runtime.emitContract`.

## Code Boundaries

- Keep CLI parsing and orchestration in `internal/app`; inject I/O, clocks, sleep, HTTP, environment, and Keychain
  dependencies through `Dependencies` so command tests remain deterministic.
- Keep HTTP construction, response bounds, retry policy, diagnostics, and URL redaction in `internal/transport`. Write
  requests get one attempt; do not broaden retry behavior without explicit evidence that it is safe.
- Keep ambiguous-JSON rejection in `internal/strictjson`, JSON-RPC envelope rules in `internal/jsonrpc`, stdout selection
  and encoding in `internal/output`, and cross-provider consistency checks in `internal/evidence`. Do not duplicate these
  checks in command handlers.

## Development Workflow

- After editing CLI source code, proactively run `just install-cli` to refresh the globally installed `routemesh`
  binary so it reflects the change.
- Prefer the `justfile`: `just build` builds `bin/routemesh`, `just test` runs the Go test suite, and `just lint` runs
  `golangci-lint`.
- Run `just check` for the complete local acceptance suite (build, test, lint) before considering a change done.
- Use `just smoke CHAIN_ID` only for an intentional live check: it requires an active credential source and makes
  RouteMesh requests. Unit tests must use injected dependencies and must not require credentials or live services.
