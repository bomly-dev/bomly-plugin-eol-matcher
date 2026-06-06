BINARY ?= bomly-plugin-eol-matcher

.PHONY: test build

test:
	go test ./...

build:
	go build -o bin/$(BINARY) .
