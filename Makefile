# Makefile for Nakama EVR

COMMIT=$(shell git rev-parse --short HEAD)
VERSION=$(shell git describe --dirty=+ --tags --always)

SRC_FILES=$(shell find . -type f -name '*.go')
SRC_DIRS=$(shell find . -type d -name '*.go' | sed 's/\/[^/]*$$//')
PWD=$(shell pwd)

.PHONY: docker dist

all: nakama

nakama: $(SRC_FILES)
	GOWORK=off CGO_ENABLED=1 CGO_CFLAGS="-O0 -g" go build \
				 -trimpath -mod=vendor -gcflags "-trimpath $(PWD)" -gcflags="all=-N -l" \
				 -asmflags "-trimpath $(PWD)" \
				 -ldflags "-s -X main.version=$(VERSION) -X main.commitID=$(COMMIT)" \
				 -o nakama


dev: $(SRC_FILES)
	docker build -t echotools/nakama:dev . -f build/Dockerfile.local

release: $(SRC_FILES)
	# if the git commit isn't a tag, we don't want to build a release
	@if [ -z "$(shell git describe --tags --exact-match 2>/dev/null)" ]; then \
		echo "Not on a tag, not building a release"; \
	else \
		docker build -t echotools/nakama:latest -t echotools/nakama:$(VERSION) . -f build/Dockerfile; \
	fi
	docker push echotools/nakama:latest
	docker push echotools/nakama:$(VERSION)
