BINARY ?= bomly-plugin-eol-lifecycle

.PHONY: test build

test:
	go test ./...

build:
	go build -o bin/$(BINARY) .
