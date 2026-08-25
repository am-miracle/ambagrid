# Senior Engineering Practices

*Principles for building reliable, maintainable infrastructure at AmbaGrid.*

## Core Principles

| Principle | Why | Enforced By |
|-----------|-----|-------------|
| **Defensive by default** | Infrastructure faces unpredictable loads and failures | Validate inputs, check errors, assume nothing about upstream |
| **Observability first** | Incidents happen; debug quickly | Structured logging, metrics, clear error types |
| **Small, reviewable changes** | High-quality reviews, low regressions | One logical change per PR; no "kitchen sink" refactors |
| **Backward compatibility** | Field operators depend on stable contracts | Deprecation periods, versioned APIs, graceful fallbacks |
| **Testability** | Tests are the safety net for refactoring | Prefer composition over hidden state; write tests before code when possible |

---

## Language Conventions

Each language has a "lean" check list. Pass all before push.

### Go

- `gofmt -w .` — formatter is non-negotiable
- MQTT callbacks return quickly — never block on DB/Kafka in Paho path
- Context deadlines around every network call (`context.WithTimeout(5s)` default)
- Preserve raw device payloads until a service owns schema conversion
- Error wrap with `fmt.Errorf("doing X: %w", err)` — preserves the chain for
  `errors.Is`/`errors.As`; it does not capture a stack trace, so pair it with
  structured logging or your tracing layer if incident debugging needs one
- Test with `go test -race ./...` — data races are hard to track post-deploy

### Rust

- `cargo fmt` and `cargo clippy -- -D warnings` clean before push
- Explicit, testable control flow — no hidden global state in the grid engine
- Use `anyhow` or `thiserror` for error handling — consistency across services
- Prefer buffered I/O for MQTT payload streaming
- `cargo test --all-targets --all-features` in CI

### TypeScript / React

- Biome check (`npm run check`) passes on every commit
- Production builds pass (`npm run build`)
- Predictable UI state — explicit stores over scattered `useState`
- No decorative copy that hides operational status
- Type every prop interface — `no any` in production components

---

## Pre-PR Checklist

- [ ] **One logical change** — diff solves one problem, not a collection of refactors
- [ ] **Tests added/updated** — behavior changes have corresponding tests; all CI passes
- [ ] **Formatting clean** — `gofmt`, `cargo fmt`, `biome check` all pass with no errors
- [ ] **CI green** — branch passes CI before PR opened
- [ ] **No secrets in diff** — scan `git diff --cached`; verify no credentials appear
- [ ] **Documentation updated** — user-visible changes reflected in docs; internal changes have a one-line comment
- [ ] **Rollback path considered** — can this revert without data loss or outage? Explain why not if no
- [ ] **Performance impact checked** — high-frequency paths (MQTT, telemetry) haven't added hidden blocking work

---

## What "Senior" Means Here

Not about the trendiest framework or 100% test coverage. It is about:

- **Reliability** — the system works when things go wrong
- **Maintainability** — a new engineer can reason about the code in reasonable time
- **Operability** — incidents diagnosed and resolved without guesswork
- **Judicious tradeoffs** — technical debt explicitly documented and scheduled

Senior engineers mentor by example: clean diffs, thoughtful PR descriptions, and a willingness to say "let's keep this simple" when complexity doesn't buy real value.
