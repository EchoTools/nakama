# Makefile for Nakama EVR

COMMIT=$(shell git rev-parse --short HEAD)
GIT_DESCRIBE=$(shell git describe --dirty=+ --tags --always)
TAG=$(shell git describe --tags --exact-match 2>/dev/null || echo "dev")
SRC_FILES=$(shell find . -type f -name '*.go')
SRC_DIRS=$(shell find . -type d -name '*.go' | sed 's/\/[^/]*$$//')
PWD=$(shell pwd)

.PHONY: docker dist

all: nakama

nakama: $(SRC_FILES)
	GOWORK=off CGO_ENABLED=1 CGO_CFLAGS="-O0 -g" go build \
		-trimpath -mod=vendor -gcflags "-trimpath $(PWD)" -gcflags="all=-N -l" \
		-asmflags "-trimpath $(PWD)" \
		-ldflags "-s -X main.version=$(GIT_DESCRIBE) -X main.commitID=$(COMMIT)" \
		-o nakama

build: $(SRC_FILES)
	docker buildx build -t echotools/nakama:$(TAG) . -f build/Dockerfile.local
	docker tag echotools/nakama:$(TAG) echotools/nakama:latest


release: $(SRC_FILES)
	@if [ "$(TAG)" == "dev" ]; then \
		echo "Not on a tag, not building a release"; \
		exit 1; \
	else \
		docker buildx build -t echotools/nakama:latest -t echotools/nakama:$(TAG) . -f build/Dockerfile; \
	fi

push: release
	docker push echotools/nakama:$(TAG) echotools/nakama:latest
