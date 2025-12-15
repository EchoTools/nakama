# Makefile for Nakama EVR

COMMIT=$(shell git rev-parse --short HEAD)
GIT_DESCRIBE=$(shell git describe --tags --always --abbrev=7 --dirty)
TAG=$(shell git describe --tags --exact-match 2>/dev/null || echo "dev")
SRC_FILES=$(shell find . -type f -name '*.go')
SRC_DIRS=$(shell find . -type d -name '*.go' | sed 's/\/[^/]*$$//')
PWD=$(shell pwd)

DEBUG_FLAGS=-trimpath -mod=vendor -gcflags "-trimpath $(PWD)" -gcflags="all=-N -l" -asmflags "-trimpath $(PWD)"
RELEASE_FLAGS=-trimpath -mod=vendor -gcflags "-trimpath $(PWD)" -asmflags "-trimpath $(PWD)"
.PHONY: all dev release push bench-baseline bench-compare bench-check

all: nakama

nakama: dev

dev: $(SRC_FILES)
	GOWORK=off CGO_ENABLED=1 CGO_CFLAGS="-O0 -g" go build \
		$(DEBUG_FLAGS) \
		-ldflags "-X main.version=$(GIT_DESCRIBE) -X main.commitID=$(COMMIT)" \
		-o nakama-debug

build: $(SRC_FILES)
		docker buildx build \
			--build-arg VERSION=$(GIT_DESCRIBE) \
			-t echotools/nakama:$(TAG) . -f build/Dockerfile.local

release: build
	@if [ "$(TAG)" == "dev" ]; then \
		echo "Not on a tag, not building a release"; \
		exit 1; \
	else \
		docker buildx build --push \
			--build-arg VERSION=$(GIT_DESCRIBE) \
			-t echotools/nakama:latest . -f build/Dockerfile.local; \
	fi

# Benchmark targets
bench-baseline:
	@echo "Creating benchmark baseline (this takes ~30 seconds)..."
	@mkdir -p _benchmarks
	@go test -run='^$$' -bench='BenchmarkPredictOutcomes$$' -benchmem -count=5 ./server/ 2>&1 | \
		grep -E '^(goos|goarch|pkg|cpu|Benchmark|PASS|ok)' > _benchmarks/predict_outcomes_baseline.txt
	@echo "Baseline saved to _benchmarks/predict_outcomes_baseline.txt"
	@$$(go env GOPATH)/bin/benchstat _benchmarks/predict_outcomes_baseline.txt || \
		(echo "Installing benchstat..." && go install golang.org/x/perf/cmd/benchstat@latest && \
		$$(go env GOPATH)/bin/benchstat _benchmarks/predict_outcomes_baseline.txt)

bench-compare:
	@./scripts/bench-compare.sh

bench-check: bench-compare
	@echo "Benchmark regression check passed"
