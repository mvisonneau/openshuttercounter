NAME          := openshuttercounter
COVERAGE_FILE := coverage.out
REPOSITORY    := mvisonneau/$(NAME)

GOLANGCI_LINT := go tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint
GORELEASER    := go tool github.com/goreleaser/goreleaser/v2

# Building the linter once lets it be re-run under a different GOOS. `go tool`
# would cross compile the linter itself instead of the code under it.
GOLANGCI_BIN  := .bin/golangci-lint
PLATFORMS     := linux darwin windows

.DEFAULT_GOAL := help

.PHONY: fmt
fmt: ## Format source code
	$(GOLANGCI_LINT) fmt -v

.PHONY: lint
lint: ## Run all lint related tests upon the codebase
	$(GOLANGCI_LINT) run -v

.PHONY: lint-all
lint-all: $(GOLANGCI_BIN) ## Lint every platform's USB backend, not only the host's
	@for os in $(PLATFORMS); do \
		echo "==> GOOS=$$os"; \
		GOOS=$$os $(GOLANGCI_BIN) run || exit 1; \
	done

$(GOLANGCI_BIN):
	@mkdir -p $(dir $@)
	go build -o $@ github.com/golangci/golangci-lint/v2/cmd/golangci-lint

.PHONY: test
test: ## Run the tests against the codebase
	@rm -rf $(COVERAGE_FILE)
	go test -v -count=1 -race ./... -coverprofile=$(COVERAGE_FILE)
	@go tool cover -func $(COVERAGE_FILE) | awk '/^total/ {print "coverage: " $$3}'

.PHONY: coverage
coverage: ## Prints coverage report
	go tool cover -func $(COVERAGE_FILE)

.PHONY: coverage-html
coverage-html: ## Generates coverage report and displays it in the browser
	go tool cover -html=$(COVERAGE_FILE)

.PHONY: install
install: ## Build and install locally the binary (dev purpose)
	go install ./cmd/$(NAME)

.PHONY: build
build: ## Build the binary using local GOOS
	go build ./cmd/$(NAME)

.PHONY: build-all
build-all: ## Cross compile every supported target, to check the backends still build
	GOOS=linux   GOARCH=amd64 go build -o /dev/null ./cmd/$(NAME)
	GOOS=linux   GOARCH=arm64 go build -o /dev/null ./cmd/$(NAME)
	GOOS=linux   GOARCH=386   go build -o /dev/null ./cmd/$(NAME)
	GOOS=darwin  GOARCH=amd64 go build -o /dev/null ./cmd/$(NAME)
	GOOS=darwin  GOARCH=arm64 go build -o /dev/null ./cmd/$(NAME)
	GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/$(NAME)
	GOOS=windows GOARCH=386   go build -o /dev/null ./cmd/$(NAME)

.PHONY: release
release: ## Build & release the binaries
	$(GORELEASER) release --clean

.PHONY: snapshot
snapshot: ## Build the release artifacts without publishing anything
	$(GORELEASER) release --snapshot --clean

.PHONY: autocomplete-scripts
autocomplete-scripts: ## Download CLI autocompletion scripts
	rm -rf helpers/autocomplete
	mkdir -p helpers/autocomplete
	go run ./cmd/$(NAME) completion bash > helpers/autocomplete/bash
	go run ./cmd/$(NAME) completion fish > helpers/autocomplete/fish
	go run ./cmd/$(NAME) completion zsh  > helpers/autocomplete/zsh

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf $(NAME) $(COVERAGE_FILE) .bin dist helpers

.PHONY: is-git-dirty
is-git-dirty: ## Tests if git is in a dirty state
	@git status --porcelain
	@test $(shell git status --porcelain | grep -c .) -eq 0

.PHONY: all
all: lint-all test build-all coverage ## Lint every platform, test and cross compile

.PHONY: help
help: ## Displays this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
