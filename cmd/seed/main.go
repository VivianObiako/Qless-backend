// Command seed creates a demo queue with a few customers already waiting, so
// the app can be shown to someone within seconds of starting it.
//
// Each run creates a fresh queue. Owner tokens are only ever returned once, so
// reusing an existing demo queue would leave you unable to open its dashboard.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/vivianobiako/qless/api/internal/config"
	"github.com/vivianobiako/qless/api/internal/database"
	"github.com/vivianobiako/qless/api/internal/storage"
	"github.com/vivianobiako/qless/api/internal/token"
)

const webURL = "http://localhost:3000"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := database.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}

	ctx := context.Background()
	store, err := storage.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	ownerToken, err := token.New()
	if err != nil {
		return err
	}
	recoveryCode, err := token.NewCode()
	if err != nil {
		return err
	}

	capacity := 30
	created, err := store.CreateQueue(ctx, storage.CreateQueueParams{
		Name:                     "Ade's Barbershop",
		Description:              "Walk-ins welcome",
		AverageServiceMinutes:    15,
		MaxCapacity:              &capacity,
		NewOwnerTokenHash:        token.Hash(ownerToken),
		NewOwnerRecoveryCodeHash: token.HashCode(recoveryCode),
	})
	if err != nil {
		return err
	}
	queue := created.Queue

	for _, name := range []string{"Vivian", "John", "Sarah", "David"} {
		customerToken, err := token.New()
		if err != nil {
			return err
		}
		entry, err := store.Join(ctx, queue.ID, name, token.Hash(customerToken))
		if err != nil {
			return fmt.Errorf("seed %s: %w", name, err)
		}
		fmt.Printf("  #%d  %s\n", entry.Number, entry.CustomerName)
	}

	fmt.Printf("\nSeeded %q with 4 customers waiting.\n\n", queue.Name)
	fmt.Printf("  Customer view   %s/q/%s\n", webURL, queue.Slug)
	fmt.Printf("  Dashboard       %s/dashboard/%s?k=%s\n", webURL, queue.ID, ownerToken)
	fmt.Printf("  Recovery code   %s\n", recoveryCode)
	fmt.Printf("\nOpen the customer view on a phone and the dashboard on your laptop.\n")

	return nil
}
