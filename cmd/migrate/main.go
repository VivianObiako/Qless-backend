package main

import (
	"fmt"
	"os"

	"github.com/vivianobiako/qless/api/internal/config"
	"github.com/vivianobiako/qless/api/internal/database"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if err := database.Migrate(cfg.DatabaseURL); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Println("migrations applied")

	// The test database is migrated alongside the primary one so `make test`
	// works straight after `make up` without a second command.
	if testURL := config.TestDatabaseURL(); testURL != "" {
		if err := database.Migrate(testURL); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not migrate test database:", err)
			return
		}
		fmt.Println("test database migrated")
	}
}
