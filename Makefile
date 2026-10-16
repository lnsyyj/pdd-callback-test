APP_NAME := pdd-callback-test
BUILD_DIR := bin
GO ?= go

.PHONY: all build run fmt test clean

all: build

build:
	$(GO) build -o $(BUILD_DIR)/$(APP_NAME) .

run:
	$(GO) run . -config config.json

fmt:
	$(GO) fmt ./...

test:
	$(GO) test ./...

clean:
	rm -rf $(BUILD_DIR)
