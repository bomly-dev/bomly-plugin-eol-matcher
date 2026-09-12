# AGENTS.md

Guidance for coding agents working in this repository. `CLAUDE.md` is an
identical copy; keep the two files in sync.

This repository is a Bomly plugin. It compiles against
`github.com/bomly-dev/bomly-sdk` and is consumed by `bomly-dev/bomly-cli` as a
pinned module. The release order is **SDK tags first, plugins adopt, then the
CLI pins** — so a stale pin here stalls the CLI behind it.

## The modernizer, and the analyzers we decline

`go fix ./...` is the Go 1.27 modernizer. It **applies** its rewrites in place;
`go fix -diff ./...` prints them instead, which is how to look first.

Two of its analyzers are declined across every Bomly repository. Run it as:

```sh
go fix -embedlit=false -omitzero=false ./...
```

- **`embedlit`** flattens `Coordinates: sdk.Coordinates{...}` into the bare
  promoted fields at construction sites. `Coordinates` is a named identity
  concept (ADR-0041 in bomly-cli), and the wrapper is what keeps the identity
  visible where a package is built. This is not hypothetical here: when the
  modernizer was first run across the plugin fleet, `embedlit` proposed
  rewrites in five of fourteen repositories, and in `bomly-plugin-template` it
  was the *only* thing the modernizer found — an unqualified run there would
  have propagated flattened coordinates into every plugin created from the
  template.
- **`omitzero`** drops `omitempty` from struct-valued JSON fields. The encoded
  bytes do not move — `encoding/json` never omits a struct — so no test and no
  linter objects. But the tag *is* the wire schema for `bomly.plugin.v1`, and a
  consumer generating a schema by reflection then reads the field as required.
  `bomly-sdk` took that rewrite once and spent four review rounds undoing it.

Neither is caught by a test, a linter, or CI. That is exactly why the decision
is written down here rather than left in a commit message.

## Conventions

- The SDK owns shared domain meaning: package URLs (`purlkit`), SPDX
  expressions (`spdxkit`), digest algorithms, ecosystem joins. If you find
  yourself writing a table that maps one vocabulary to another, check the SDK
  first — several drifted copies of exactly that have been found and deleted
  across these repositories.
- Do not commit build output. Binaries belong in `bin/` or `dist/`, both
  ignored.
