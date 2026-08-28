SHELL := /bin/bash
.DEFAULT_GOAL := help

.PHONY: help up down api seed test migrate reset-db

help: ## Show available commands
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

up: ## Start Postgres and wait for it to be ready
	docker compose up -d --wait

down: ## Stop Postgres
	docker compose down

reset-db: ## Destroy and recreate the database volume
	docker compose down -v
	docker compose up -d --wait

migrate: ## Apply pending migrations
	go run ./cmd/migrate

api: ## Run the Go API server
	go run ./cmd/server

seed: ## Create the demo queue (Ade's Barbershop)
	go run ./cmd/seed

test: ## Run the test suite
	go test ./... -count=1
