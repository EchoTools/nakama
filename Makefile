# Makefile for Nakama EVR

COMMIT=$(shell git rev-parse --short HEAD)
GIT_DESCRIBE=$(shell git describe --tags --always --abbrev=7 --dirty)
TAG=$(shell git describe --tags --exact-match 2>/dev/null || echo "dev")
SRC_FILES=$(shell find . -type f -name '*.go')
SRC_DIRS=$(shell find . -type d -name '*.go' | sed 's/\/[^/]*$$//')
PWD=$(shell pwd)

DEBUG_FLAGS=-trimpath -mod=vendor -gcflags "-trimpath $(PWD)" -gcflags="all=-N -l" -asmflags "-trimpath $(PWD)"
RELEASE_FLAGS=-trimpath -mod=vendor -gcflags "-trimpath $(PWD)" -asmflags "-trimpath $(PWD)"
.PHONY: all dev release push bench-baseline bench-compare bench-check \
	act act-build act-tests act-list act-lint

all: nakama

nakama: $(SRC_FILES)
	GOWORK=off CGO_ENABLED=1 CGO_CFLAGS="-O0 -g" go build \
		$(DEBUG_FLAGS) \
		-ldflags "-X main.version=$(GIT_DESCRIBE) -X main.commitID=$(COMMIT)" \
		-o nakama

build: $(SRC_FILES)
		docker buildx build \
			--build-arg VERSION=$(GIT_DESCRIBE) \
			-t ghcr.io/echotools/nakama:$(TAG) . -f build/Dockerfile.local

release: build
	@if [ "$(TAG)" == "dev" ]; then \
		echo "Not on a tag, not building a release"; \
		exit 1; \
	else \
		docker buildx build --push \
			--build-arg VERSION=$(GIT_DESCRIBE) \
			-t ghcr.io/echotools/nakama:latest . -f build/Dockerfile.local; \
	fi

# Benchmark targets
bench-baseline:
	@echo "Creating benchmark baseline (this takes ~30 seconds)..."
	@mkdir -p _benchmarks
	@go test -run='^$$' -bench='BenchmarkPredictOutcomes$$' -benchmem -count=6 ./server/ 2>&1 | \
		grep -E '^(goos|goarch|pkg|cpu|Benchmark|PASS|ok)' > _benchmarks/predict_outcomes_baseline.txt
	@echo "Baseline saved to _benchmarks/predict_outcomes_baseline.txt"
	@$$(go env GOPATH)/bin/benchstat _benchmarks/predict_outcomes_baseline.txt || \
		(echo "Installing benchstat..." && go install golang.org/x/perf/cmd/benchstat@latest && \
		$$(go env GOPATH)/bin/benchstat _benchmarks/predict_outcomes_baseline.txt)

bench-compare:
	@./scripts/bench-compare.sh

bench-check: bench-compare
	@echo "Benchmark regression check passed"

# GitHub Actions local testing with act
# Use medium image for better compatibility (default is too minimal)
ACT_FLAGS ?= --container-architecture linux/amd64

act-list: ## List all available GitHub Actions workflows and jobs
	@act -l

act-build: ## Run the build workflow locally
	@act -j build_binary $(ACT_FLAGS)

act-tests: ## Run the tests workflow locally
	@act -j run_tests $(ACT_FLAGS)

act-lint: ## Validate GitHub Actions workflow syntax
	@command -v actionlint >/dev/null 2>&1 || (echo "Installing actionlint..." && go install github.com/rhysd/actionlint/cmd/actionlint@latest)
	@actionlint .github/workflows/*.yml .github/workflows/*.yaml

act: act-list ## Alias for act-list (show available workflows)
