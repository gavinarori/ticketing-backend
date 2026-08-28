package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgErrorCode extracts a PostgreSQL error code (e.g. "23505" for a unique
// violation) from err, or "" if err isn't a *pgconn.PgError. Repository
// methods use this to turn specific constraint violations into the
// appropriate domain sentinel (domain.ErrConflict, etc.) instead of
// leaking a raw driver error up through the service layer.
func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

const (
	pgCodeUniqueViolation     = "23505"
	pgCodeForeignKeyViolation = "23503"
)
