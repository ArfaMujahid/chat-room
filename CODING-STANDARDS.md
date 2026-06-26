# Coding Standards & Best Practices

**Project:** Concurrent Web Scraper (and the two projects after it)
**Purpose:** The single source of truth for *how* we write Go here. Generated code
(AI or otherwise) is reviewed against this document. If code violates a rule, the
code is wrong, not the rule.

> How to use this: skim it once now, keep it open while building, and run the
> **§14 review checklist** against every chunk of code before committing.

---

## 0. The headline rule — document every function

**Every function and method gets a 1–2 line comment explaining what it does and,
where non-obvious, *why*.** No exceptions, including unexported helpers.

Follow Go's convention: the comment sits **directly above** the declaration, is a
**complete sentence**, and **begins with the function's name**.

```go
// Good — exported function

// Fetch retrieves the page at url, retrying transient failures with backoff.
// It returns an error if the context is cancelled or all retries are exhausted.
func (f *Fetcher) Fetch(ctx context.Context, url string) (*Response, error) { ... }

// Good — unexported helper (still documented, can be one line)

// hostOf extracts the lowercased hostname from a URL, used as the rate-limit key.
func hostOf(u string) string { ... }
```

```go
// Bad — no comment
func (f *Fetcher) Fetch(ctx context.Context, url string) (*Response, error) { ... }

// Bad — restates the signature, adds nothing
// Fetch fetches.
func (f *Fetcher) Fetch(...) { ... }

// Bad — doesn't start with the name (breaks `go doc` / pkg.go.dev rendering)
// This function fetches a page.
func (f *Fetcher) Fetch(...) { ... }
```

Why this rule, for this project specifically:
- It forces *you* to understand AI-generated code line by line (your stated goal).
- The name-prefixed form is what `go doc` and pkg.go.dev surface, so the docs are real.
- The "why" on non-obvious functions is where the value is — explain the *decision*,
  not the mechanics the code already shows.

Two refinements so it stays useful rather than noise:
- For genuinely trivial one-liners (a plain getter), a short single line is fine —
  don't pad it. Quality over ceremony.
- **Package-level doc comment is also required:** one comment above the `package`
  clause in each package's primary file, describing the package's purpose.

```go
// Package fetcher provides a context-aware HTTP client with retries, timeouts,
// and a bounded response size, used by the crawler to retrieve pages.
package fetcher
```

---

## 1. Formatting & Tooling (non-negotiable, automated)

Formatting is not a matter of taste — it's a tool's job. These run in CI and
ideally on save:

| Tool | What it enforces | Command |
|------|------------------|---------|
| `gofmt` / `goimports` | Canonical formatting + import grouping/ordering | `gofmt -l .` (lists unformatted), `goimports -w .` |
| `go vet` | Real bugs: printf mismatches, copied locks, bad struct tags | `go vet ./...` |
| `golangci-lint` | Aggregated linters (errcheck, staticcheck, ineffassign, …) | `golangci-lint run ./...` |
| `govulncheck` | Known vulns in *called* code paths | `govulncheck ./...` |
| `go mod tidy` | Dependencies match imports; no unused/missing | `go mod tidy` then verify clean |
| race detector | Data races in tests | `go test -race ./...` |

Rules:
- **Never** hand-format to fight `gofmt`. There is one true format.
- A PR/commit that isn't `gofmt`-clean or fails `go vet` does not land.
- Run `go mod tidy` before committing; `go.mod`/`go.sum` stay tidy.

---

## 2. Naming

- **Packages:** short, lowercase, single word, no underscores/camelCase, singular —
  `fetcher`, `crawler`, `ratelimit`. Name matches the directory.
- **Avoid stutter:** the package qualifier is already there. Use `crawler.New`, not
  `crawler.NewCrawler`; `job.ID`, not `job.JobID` *inside* the `job` package.
- **Exported vs unexported = capitalization** (Lesson 1). Default to unexported;
  export only what other packages genuinely need. Exported = public API contract.
- **Interfaces:** single-method interfaces are named by the method + `-er`
  (`Fetcher`, `Writer`, `Reader`). Define them where they're *consumed* (Lesson 4).
- **Constructors:** `New` (or `NewX`) returning the concrete type or `*T`.
- **Errors:** sentinel vars are `ErrXxx` (`ErrNotFound`); error types are `XxxError`.
- **Acronyms keep case:** `URL`, `HTTP`, `ID` — `parseURL`, `userID`, not `parseUrl`.
- **Short names for short scopes:** `i`, `r`, `ctx`, `err` are fine in tight scopes;
  use descriptive names for wider scopes and exported identifiers.

---

## 3. Error Handling (Lesson 5)

- **Errors are values, returned as the last return.** Check `if err != nil`
  immediately; the other returns are invalid when `err != nil`.
- **Wrap with context as errors travel up:** `fmt.Errorf("fetching %s: %w", url, err)`.
  Use `%w` (preserves the chain), not `%v`. Add the *operation*, not the word "error".
- **Inspect with `errors.Is` / `errors.As`, never `==`** — wrapping breaks `==`.
- **Handle each error exactly once.** Lower layers wrap-and-return; the top boundary
  (handler / `main`) logs once. No log-and-return at every layer.
- **Don't silently ignore errors** (`x, _ := …`). If you must, it's a conscious,
  rare, commented decision. `errcheck` will flag the rest.
- **Error strings:** lowercase, no trailing punctuation (`"file not found"`), because
  they get composed into larger messages.
- **Never return a typed nil as `error`** (the typed-nil trap, Lesson 4/5). Return
  literal `nil` for success; only return a value when there's a real error.
- **`panic` is for unrecoverable programmer bugs only.** Library/package code returns
  errors. The one allowed `recover` is a top-level guard (per-request HTTP handler,
  per-job goroutine) so one failure doesn't kill the process (NFR-R3).

---

## 4. Concurrency (Lesson 6 — the project's core)

- **Before writing `go`, answer: how does this goroutine stop?** Every goroutine
  needs a guaranteed exit via `context` cancellation or a closed channel (NFR-R1).
- **Thread `context.Context` as the first parameter, named `ctx`.** Workers,
  fetcher, rate limiter, janitor all `select { case <-ctx.Done(): … }`.
- **Bound concurrency** — worker pool, never goroutine-per-URL (NFR-C1).
- **All shared state is synchronized.** Visited set and registry: `map` guarded by a
  `sync.Mutex`/`RWMutex`. Counters: `sync/atomic`. Code must pass `-race` (NFR-C2).
  An unsynchronized shared variable is a bug even if it "works."
- **Only the sender closes a channel.** Closing a closed/nil channel, or sending on
  a closed one, panics.
- **Never copy a `sync.Mutex`/`WaitGroup`** (or a struct containing one) — use pointer
  receivers and pass pointers. `go vet` catches many cases.
- **`RWMutex` only for read-heavy state** (registry, served to SSE); plain `Mutex`
  otherwise.
- **Use `errgroup`** for "run N things, fail fast, cancel the rest" instead of
  hand-rolling WaitGroup + error channel.
- **Buffer result/event channels** appropriately so a producer never blocks forever
  on a consumer that has gone away (leak avoidance).

---

## 5. Interfaces & Types (Lessons 3, 4)

- **Accept interfaces, return concrete types.** Functions take `io.Reader`,
  `Fetcher`, `Writer`; constructors return `*Crawler`, `*Fetcher`.
- **Keep interfaces small** — one method is ideal, and most testable.
- **Define interfaces at the consumer**, not next to the implementation. The crawler
  defines the `Fetcher` interface it needs; the fetcher package just satisfies it.
- **Receiver consistency:** pick pointer *or* value receivers per type and don't mix.
  Default to pointer receivers for structs (and required when mutating or holding a
  mutex).
- **Make the zero value useful** where practical (Lesson 1) — but a type that needs a
  map/channel initialized gets a `New` constructor (a nil map write panics).
- **Use named-field struct literals** (`Config{Workers: 10}`), never positional.
- **`struct{}` for set values and signal channels** (zero bytes) — e.g. the visited
  set `map[string]struct{}`, the done channel `chan struct{}`.

---

## 6. Resource Management

- **`defer Close()` immediately after acquiring** a resource (file, HTTP body):
  ```go
  resp, err := client.Do(req)
  if err != nil { return err }
  defer resp.Body.Close()        // always, right after the error check
  ```
- **HTTP client:** one shared `*http.Client` with a `Timeout`, reused; never the
  default client for external calls; drain unread bodies for connection reuse (L10).
- **HTTP server:** explicit `http.Server` with `ReadTimeout`/`WriteTimeout`/
  `IdleTimeout`; never `http.ListenAndServe(addr, mux)` for anything real (L10).
- **Flush buffered writers** (`bufio.Writer`, `csv.Writer`) on close — forgetting
  this silently drops data (the output writer's flush-on-close is mandatory).
- **`Stop()` every `time.Ticker`** (the janitor's), or it leaks.
- **Cap untrusted input:** `io.LimitReader` on response bodies (NFR-S2). Don't
  `io.ReadAll` an unbounded remote stream.

---

## 7. Project Structure (Lessons 7, 12)

- **`cmd/<binary>/main.go`** is the only entry point and the composition root — thin
  orchestration (flags → wire dependencies → run). No business logic.
- **`internal/`** holds all logic, compiler-blocked from outside import. Organize by
  **domain** (`fetcher`, `crawler`, `job`), not technical layer.
- **No import cycles** (hard compile error). If you hit one, extract shared types to a
  leaf package or invert the dependency with a consumer-side interface.
- **Dependency injection via constructors**, wired in `main`. Layers depend on
  interfaces for collaborators so tests inject fakes.
- **One package per directory**; split a package across files only for human
  organization.

---

## 8. Testing (Lesson 9)

- **Table-driven tests with `t.Run` subtests** are the default shape.
- **Test through interfaces with hand-written fakes** (fake `Fetcher`) — no real
  network/disk in unit tests. Use `httptest` for HTTP-layer tests.
- **Cover the concurrency paths under `-race`** (NFR-C2, M2).
- **`t.Helper()`** in assertion helpers; **`t.Cleanup()`** for teardown;
  **`t.TempDir()`** for files (the output/cleanup tests).
- **Use `cmp.Diff`** for comparing structs/slices/maps, not `reflect.DeepEqual`.
- **Failure messages:** `got X; want Y` with enough context to debug from the message.
- **Examples with `// Output:`** for the public API double as docs that can't rot.

---

## 9. Logging (Lesson 10)

- **Use `log/slog` (structured), not `log` or `fmt.Println`,** for operational
  output. JSON handler in production-like runs, key/value fields:
  ```go
  logger.Info("job completed", "job_id", id, "pages", n, "duration_ms", ms)
  ```
- **Log at boundaries, once** (per §3). Don't log the same error at every layer.
- **No secrets or full request bodies in logs.** (We have none now, but the habit
  carries to Project 2.)
- **The dashboard does not replace logs** — it's a view; logs are the record.

---

## 10. Configuration (Lesson 12)

- **Config comes from flags/env**, has sensible defaults, and is **validated at
  startup** (`Config.Validate()`) so misconfiguration fails fast and loudly.
- **Never hardcode paths, ports, limits** — they're config with defaults.
- **Never commit secrets.** (None yet; the rule stands for later projects.)

---

## 11. Comments & Documentation (beyond §0)

- **Comment the *why*, not the *what*.** The code shows what; comments explain
  decisions, tradeoffs, and non-obvious constraints.
- **Every exported identifier (type, func, const, var) has a doc comment** starting
  with its name. Required by §0 and by Go convention.
- **`TODO(name): …` / `FIXME:`** for known gaps — searchable, attributable.
- **No commented-out code in commits.** Delete it; git remembers.
- **Explain concurrency invariants** at the struct/field level: e.g.
  `// guarded by mu` above a field, so the locking contract is documented.

---

## 12. Performance & Allocation (Lessons 3, 8 — apply with judgment)

- **Pre-allocate slices/maps with capacity hints** when the size is known
  (`make([]T, 0, n)`). Cheap, meaningful win on hot paths.
- **Stream, don't accumulate** — results go to disk via the writer, never held all in
  memory (NFR-P2).
- **Don't micro-optimize blindly.** Reach for `-gcflags='-m'` (escapes) and `pprof`
  only after a benchmark shows a real bottleneck. Readability first.
- **`strings.Builder`** for building strings in loops, not `+=`.

---

## 13. Dependencies

- **Lean tree, every dependency justified** (see the architecture doc's dependency
  table). Standard library preferred where reasonable.
- **Pin via `go.mod`; commit `go.sum`.** Don't add a dependency for something the
  stdlib does well.
- **Run `govulncheck`** before shipping.

---

## 14. Pre-Commit Review Checklist (run this on every change — esp. AI-generated)

Functionality & structure
- [ ] Does this trace to a requirement in the architecture spec? If not, why does it exist?
- [ ] Logic in `internal/`, wiring in `main`; no business logic in `main`.
- [ ] No import cycles; packages organized by domain.

Documentation
- [ ] **Every function/method has a 1–2 line, name-prefixed doc comment (§0).**
- [ ] Every exported identifier is documented; each package has a package comment.
- [ ] Comments explain *why* where non-obvious; no commented-out code.

Errors
- [ ] Every error checked; none silently ignored.
- [ ] Errors wrapped with `%w` + operation context; inspected via `Is`/`As`.
- [ ] No typed-nil returned as `error`; `panic` only for unrecoverable bugs.

Concurrency
- [ ] Every goroutine has a guaranteed stop (ctx/closed channel).
- [ ] All shared state synchronized; **passes `go test -race`**.
- [ ] No copied mutex/WaitGroup; sender (only) closes channels.

Resources
- [ ] `defer Close()` on every acquired resource; bodies drained/closed.
- [ ] Buffered writers flushed on close; tickers stopped.
- [ ] HTTP client/server have timeouts; untrusted input bounded.

Tooling
- [ ] `gofmt`-clean, `go vet` clean, `golangci-lint` clean.
- [ ] `go mod tidy` run; `go.mod`/`go.sum` tidy.
- [ ] Tests added/updated for the change; meaningful, table-driven where it fits.

---

## 15. The one-paragraph philosophy

Optimize for the next person to read this code (often future-you). Go's whole design
favors **explicit, simple, readable** code over clever brevity — lean into that.
Make decisions visible (in comments and structure), make failures impossible to
ignore (checked errors, `-race`, validation at startup), and make the invisible
visible where it matters (logs, the dashboard). Boring, obvious, well-documented Go
is the goal — it's what makes a codebase a client can trust and a maintainer can keep.
