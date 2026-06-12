.PHONY: run run-dev build test clean fmt deps help

.DEFAULT_GOAL := help

APP_NAME := oak
MAIN_PATH := ./cmd
BUILD_DIR := ./bin
GO := go
GOFLAGS := -v

## run: Run the application locally
run:
	$(GO) run $(MAIN_PATH)/main.go

## run-dev: Run in development mode (sources .env.dev)
run-dev:
	set -a && . ./.env.dev && set +a && $(GO) run $(MAIN_PATH)

## build: Build the application binary
build:
	@echo "Building $(APP_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(APP_NAME) ./cmd/main.go
	@echo "Binary created at $(BUILD_DIR)/$(APP_NAME)"

## test: Run all tests
test:
	$(GO) test -v -race ./...

## clean: Remove build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	$(GO) clean

## fmt: Format Go code
fmt:
	$(GO) fmt ./...

## notify: Send notification for most recent complete day (daily|weekly|both, --dry-run)
notify:
	$(GO) run ./cmd/notify $(ARGS)

## deps: Download dependencies
deps:
	$(GO) mod download
	$(GO) mod tidy

## help: Show this help message
help:
	@echo "$(APP_NAME) - Makefile commands"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':'
