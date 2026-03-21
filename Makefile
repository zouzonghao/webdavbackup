BINARY_NAME=webdav-backup
VERSION=1.0.0
BUILD_DIR=build
GO=go
GOOS=linux
GOARCH=amd64

.PHONY: all build clean install deps

all: deps build

deps:
	$(GO) mod download
	$(GO) mod tidy

build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -ldflags="-s -w -extldflags '-static'" -o $(BUILD_DIR)/$(BINARY_NAME) .

build-musl:
	docker run --rm -v "$(PWD):/src" -w /src alpine:latest sh -c "\
		apk add --no-cache go git musl-dev && \
		CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w -linkmode external -extldflags "-static"' -o $(BUILD_DIR)/$(BINARY_NAME) ."

build-docker:
	docker build -t $(BINARY_NAME)-builder .
	docker run --rm -v "$(PWD)/$(BUILD_DIR):/output" $(BINARY_NAME)-builder

clean:
	rm -rf $(BUILD_DIR)
	$(GO) clean

install: build
	install -m 755 $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)
	mkdir -p /etc/webdav-backup
	mkdir -p /var/log

uninstall:
	rm -f /usr/local/bin/$(BINARY_NAME)
	rm -rf /etc/webdav-backup

test:
	$(GO) test -v ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...
