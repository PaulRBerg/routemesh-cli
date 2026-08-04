# Context

`routemesh-cli` is a thin, deterministic Go client for the RouteMesh JSON-RPC service. Its primary interface is bounded
JSON suitable for shell pipelines and coding agents; keep stdout strictly valid JSON (or NDJSON) on success and route
anything decorative (banners, prompts) to stderr.

## Development Workflow

- After editing CLI source code, proactively run `just install-cli` to refresh the globally installed `routemesh`
  binary so it reflects the change.
- Prefer the `justfile`: `just build` builds `bin/routemesh`, `just test` runs the Go test suite, and `just lint` runs
  `golangci-lint`.
- Run `just check` for the complete local acceptance suite (build, test, lint) before considering a change done.
