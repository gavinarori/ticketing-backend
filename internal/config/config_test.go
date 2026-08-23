package config

import (
	"os"
	"testing"
)

// setRequiredEnv sets the minimum env vars needed for Load() to succeed,
// mirroring what a real deployment must always provide.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	vars := map[string]string{
		"DATABASE_URL":       "postgres://user:pass@localhost:5432/db?sslmode=disable",
		"JWT_SECRET":         "0123456789abcdef",
		"JWT_REFRESH_SECRET": "fedcba9876543210",
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
	_ = os.Unsetenv("APP_ENV") // exercise the default
}

func TestLoad_DefaultsAndValidation(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}

	if cfg.App.Env != "development" {
		t.Errorf("expected default env 'development', got %q", cfg.App.Env)
	}
	if cfg.HTTP.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.HTTP.Port)
	}
	if len(cfg.Kafka.Brokers) != 1 || cfg.Kafka.Brokers[0] != "localhost:9092" {
		t.Errorf("expected default kafka brokers [localhost:9092], got %v", cfg.Kafka.Brokers)
	}
}

func TestLoad_MissingRequired_Fails(t *testing.T) {
	// Deliberately omit DATABASE_URL and JWT secrets.
	t.Setenv("APP_ENV", "development")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when required env vars are missing, got nil")
	}
}

func TestLoad_InvalidEnum_Fails(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "not-a-real-env")

	if _, err := Load(); err == nil {
		t.Fatal("expected validation error for invalid APP_ENV, got nil")
	}
}
