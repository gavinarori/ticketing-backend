// Command migrate applies or rolls back database schema migrations from
// the ./migrations directory using golang-migrate.
//
// Usage:
//
//	go run ./cmd/migrate -direction up
//	go run ./cmd/migrate -direction down -steps 1
//	go run ./cmd/migrate -direction force -version 3
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/gavinarori/ticketing-backend/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate: fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	direction := flag.String("direction", "up", "up | down | force")
	steps := flag.Int("steps", 0, "number of steps for 'down' (0 = all)")
	version := flag.Int("version", 0, "target version for 'force'")
	migrationsPath := flag.String("path", "migrations", "path to migrations directory")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	m, err := migrate.New(
		fmt.Sprintf("file://%s", *migrationsPath),
		cfg.Postgres.DSN,
	)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil {
			fmt.Fprintln(os.Stderr, "migrate: close source:", srcErr)
		}
		if dbErr != nil {
			fmt.Fprintln(os.Stderr, "migrate: close db:", dbErr)
		}
	}()

	switch *direction {
	case "up":
		err = m.Up()
	case "down":
		if *steps > 0 {
			err = m.Steps(-*steps)
		} else {
			err = m.Down()
		}
	case "force":
		err = m.Force(*version)
	default:
		return fmt.Errorf("unknown -direction %q (want up|down|force)", *direction)
	}

	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("run migration (%s): %w", *direction, err)
	}

	fmt.Printf("migrate: %s complete (no pending changes: %v)\n", *direction, errors.Is(err, migrate.ErrNoChange))
	return nil
}
