# Vendored `purego` for `figma/statsig-server-core`

This directory vendors [`github.com/ebitengine/purego`](https://github.com/ebitengine/purego)
at upstream tag `v0.9.0`, with **one local patch** applied to work around a
concurrent-FFI return-value race in `func.go` and `syscall_sysv.go`.

## The patch

Reverts the package-wide `sync.Pool` of `*syscall15Args` (introduced by
upstream PR [#282](https://github.com/ebitengine/purego/pull/282) and
reaffirmed in [#328](https://github.com/ebitengine/purego/pull/328))
back to a per-call stack-allocated `syscall15Args`. The pool optimization
saved an allocation per call but introduced a race that lets two
concurrently-dispatching goroutines observe each other's return values —
which surfaces in `statsig-go` consumers as SIGSEGVs in
`runtime.memmove`, glibc `double free or corruption`, nil-derefs at
`*gateJson`, and silently-swapped feature-flag results.

The patched lines are marked with `FIGMA PATCH` comments in both
`func.go` (the `RegisterFunc` hot path) and `syscall_sysv.go` (the
`SyscallN` path). Diff against upstream `v0.9.0` is ~8 lines net.

## Why vendored and not a separate fork

Lives here so the existing `figma/statsig-server-core` release workflow
can cut a paired `purego/vX.Y-figmaN` tag alongside `statsig-go/vX.Y-figmaN`
and `binaries-linux-gnu/vX.Y-figmaN`. No new repo to track or sync.

## Consumer wiring

The Go module path is unchanged (`github.com/ebitengine/purego`) so that
internal imports continue to resolve. Consumers redirect via `go.mod`
`replace`:

```go
require github.com/ebitengine/purego v0.9.0

replace github.com/ebitengine/purego =>
    github.com/figma/statsig-server-core/purego v0.9.0-figma1
```

## Removing this patch

When upstream lands a fix for the underlying race, drop this directory
and the corresponding `replace` line in any consumer's `go.mod`. The
patch is intentionally minimal (only `thePool.Get`/`Put` reverted in two
files) to make that a one-line revert.

## Upstream tracking

See the open issue in `ebitengine/purego` for the discrimination matrix,
minimal repro, and proposed upstream fix. The repro is ~10 lines of C +
~80 lines of Go, completely independent of statsig.
