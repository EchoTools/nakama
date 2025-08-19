# Makefile for Nakama EVR

COMMIT=$(shell git rev-parse --short HEAD)
GIT_DESCRIBE=$(shell git describe --tags --always --abbrev=7 --dirty)
TAG=$(shell git describe --tags --exact-match 2>/dev/null || echo "dev")
SRC_FILES=$(shell find . -type f -name '*.go')
SRC_DIRS=$(shell find . -type d -name '*.go' | sed 's/\/[^/]*$$//')
PWD=$(shell pwd)

DEBUG_FLAGS=-trimpath -mod=vendor -gcflags "-trimpath $(PWD)" -gcflags="all=-N -l" -asmflags "-trimpath $(PWD)"
RELEASE_FLAGS=-trimpath -mod=vendor -gcflags "-trimpath $(PWD)" -asmflags "-trimpath $(PWD)"
.PHONY: all dev release push

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