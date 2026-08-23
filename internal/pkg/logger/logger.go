// Package logger builds the single zap.Logger instance used across the
// process. Pass it down explicitly (constructor injection) rather than
// using a package-level global — that keeps logging testable and avoids
// hidden state, per the "no global state" standard for this codebase.
package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New builds a zap.Logger appropriate for the given app environment.
//   - "production"/"staging": JSON encoding, ISO8601 timestamps, no color,
//     level filtered by `level`.
//   - anything else (local dev): human-readable console encoding with color.
func New(appEnv, level string) (*zap.Logger, error) {
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("logger: invalid log level %q: %w", level, err)
	}

	var cfg zap.Config
	if appEnv == "production" || appEnv == "staging" {
		cfg = zap.NewProductionConfig()
		cfg.EncoderConfig.TimeKey = "timestamp"
		cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	} else {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	cfg.Level = zap.NewAtomicLevelAt(lvl)

	l, err := cfg.Build()
	if err != nil {
		return nil, fmt.Errorf("logger: build: %w", err)
	}
	return l, nil
}
