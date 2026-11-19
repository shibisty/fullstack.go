# Changelog

## Unreleased

## 0.2.0 — 2026-11-19

### Changed
- Renamed from `fullstack-compress` to `@fullstack/compress` (gtr ADR-0012): `gtr add @fullstack/compress`, `import "fullstack/compress"`; published to the gtr registry. Dependencies on fullstack, passport, orm and migration packages are registry ranges instead of `github:` sources.

### Added
- `compress.New`: zstd, brotli and gzip by `Accept-Encoding` (q-values, `*`); fast levels while
  writing dynamic responses, maximal levels for responses with an ETag or `immutable`, kept in a
  bounded LRU; `Vary: Accept-Encoding`, weak ETags; streaming with `Flush`; event streams, small
  bodies, compressed types, `HEAD`, ranges and `no-transform` passed through.
