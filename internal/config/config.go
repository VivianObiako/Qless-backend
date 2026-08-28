package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL    string
	Port           string
	AllowedOrigins []string
}

// Load reads configuration from the environment, first pulling in a .env file
// from the repository root if one exists so `make api` works after a plain
// `cp .env.example .env`.
func Load() (Config, error) {
	loadDotEnv()

	cfg := Config{
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		Port:           envOr("PORT", "8080"),
		AllowedOrigins: splitOrigins(envOr("ALLOWED_ORIGIN", "http://localhost:3000")),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is not set (copy .env.example to .env)")
	}
	return cfg, nil
}

// TestDatabaseURL is used by the integration tests only.
func TestDatabaseURL() string {
	loadDotEnv()
	return os.Getenv("TEST_DATABASE_URL")
}

func loadDotEnv() {
	for _, candidate := range []string{".env", filepath.Join("..", ".env"), filepath.Join("..", "..", ".env"), filepath.Join("..", "..", "..", ".env")} {
		if _, err := os.Stat(candidate); err == nil {
			_ = godotenv.Load(candidate)
			return
		}
	}
}

// splitOrigins reads the comma-separated ALLOWED_ORIGIN into a list.
//
// A split deployment has more than one legitimate origin at once — the
// production domain plus whatever preview URL the host minted for this branch —
// and a CORS response may name only one of them, so the server has to hold the
// whole set and answer with whichever origin actually asked.
func splitOrigins(raw string) []string {
	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	return origins
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
