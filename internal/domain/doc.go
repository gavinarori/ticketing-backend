// Package domain holds the platform's core business entities and the
// repository/gateway interfaces the service layer depends on. It stays
// free of framework and infrastructure dependencies (no pgx, no chi, no
// zap) so services — and their tests — never need a real database or HTTP
// stack to compile or run.
//
// One pragmatic exception: github.com/google/uuid. It's a small,
// dependency-free wrapper around [16]byte with no framework coupling, and
// using a real UUID type (instead of a bare string) buys compile-time
// safety against passing e.g. an email where an ID was expected. Every ID
// field in this package uses uuid.UUID consistently, so if this ever
// becomes a problem it's a one-line type swap away.
package domain
