# Contributing to AmbaGrid

Thanks for helping build AmbaGrid.

This project sits close to real infrastructure. Treat every change as if a field operator might depend on it during a bad day: keep it clear, tested, and easy to roll back.

## Development Setup

Install the main toolchain:

- Docker Desktop
- Go 1.27 or newer
- Rust stable
- Node.js 22 or newer
- npm
- Protobuf compiler (`protoc`)
- Go protobuf plugin (`protoc-gen-go`): `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`

On macOS, Docker Desktop may not put the CLI on your shell path. If `docker` is not found, add:

```bash
export PATH="/Applications/Docker.app/Contents/Resources/bin:$PATH"
```

`go install` puts `protoc-gen-go` in `$(go env GOPATH)/bin`. If `protoc` reports it can't find the plugin, add that to your path:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

Start the local infrastructure:

```bash
docker compose up -d --build
```

Run the simulator:

```bash
go run scripts/virtual_meter.go
```

Open Redpanda Console:

```text
http://localhost:8080
```

## Project Layout

```text
apps/                  React control room
proto/                 shared telemetry contracts
scripts/               local developer tools and simulators
services/api-go/       Go read API (asset state, alerts)
services/ingestion-go/ MQTT to Redpanda bridge
services/engine-rust/  Rust grid engine
docs/                  architecture and hardware notes
```

## Branches

Create a branch for each change:

```bash
git checkout -b feature/mqtt-adapter
```

Use short, descriptive names:

- `feature/site-dashboard`
- `fix/mqtt-reconnect`
- `docs/hardware-adapters`

## Before You Open a Pull Request

Run the checks that match the files you changed.

For the root Go scripts:

```bash
go test ./...
```

For the ingestion service:

```bash
cd services/ingestion-go
gofmt -w .
go test ./...
```

If you change `proto/telemetry.proto`, regenerate its Go bindings and commit the result:

```bash
make proto-gen-go
```

For the API service:

```bash
cd services/api-go
gofmt -w .
go test -race ./...
```

Its layering is documented in [docs/api.md](docs/api.md): controllers own HTTP,
services own request rules, repositories own SQL. Keep new endpoints on that
path rather than querying the database from a handler.

For the Rust engine:

```bash
cd services/engine-rust
cargo fmt
cargo clippy --all-targets --all-features -- -D warnings
cargo test
```

For the frontend:

```bash
cd apps
npm install
npm run check
npm run build
```

For Docker Compose:

```bash
docker compose config --quiet
```

## Pull Request Rules

Each pull request should:

- explain the problem being solved
- describe the main design choice
- include tests for behavior changes
- keep unrelated refactors out of the diff
- pass CI before review
- include screenshots for visible frontend changes
- include sample MQTT or Kafka payloads when changing ingestion behavior

Small pull requests get reviewed faster.

## Coding Standards

Go:

- use `gofmt`
- keep MQTT callbacks fast
- do not block the Paho callback path on database or Kafka work
- prefer context deadlines around network calls
- `ingestion-go` owns telemetry schema conversion: it encodes AmbaGrid-shaped JSON into `MetricPayload` protobuf (`BuildRecord`). No other service reshapes device payloads
- preserve the original bytes when parking a rejected payload in a dead-letter topic

Rust:

- use `cargo fmt`
- keep `cargo clippy -- -D warnings` clean
- make control decisions explicit and testable
- avoid hidden global state in the grid engine

TypeScript:

- run Biome checks
- keep production builds passing
- keep UI state predictable
- avoid hiding critical grid status behind decorative UI
- prefer clear operational labels over marketing copy

Docs:

- write for a new contributor who is smart but new to mini-grid software
- explain tradeoffs plainly
- keep setup instructions runnable

## Commit Messages

Use Conventional Commits. The changelog is generated from commit history, so the prefix matters.

```text
feat(ingestion): add MQTT to Redpanda bridge
fix(simulator): fail fast when broker is unavailable
docs: document hardware adapter contract
ci: add frontend Biome check
```

Common prefixes:

- `feat`: user-visible feature
- `fix`: bug fix
- `docs`: documentation
- `ci`: workflow or automation
- `test`: tests
- `refactor`: code change without behavior change
- `chore`: maintenance

## Changelog

Install `git-cliff`, then generate the changelog from commit history:

```bash
git cliff -o CHANGELOG.md
```

Or use the Makefile target:

```bash
make changelog
```

CI runs `make changelog-check` to make sure `CHANGELOG.md` matches the generated output.
Catch this locally before it reaches CI by enabling the repo's pre-push hook once per clone:

```bash
git config core.hooksPath .githooks
```

## Security

Do not commit:

- credentials
- API keys
- production broker URLs
- customer data
- private meter payloads
- SSH keys or certificates

If you find a security issue, do not open a public issue with exploit details. Open a minimal report and ask for a private disclosure channel.

## License

By contributing, you agree that your contribution is licensed under the Apache License 2.0.
