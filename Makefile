export PATH := /usr/local/go/bin:$(PATH)

.PHONY: all build test clean install

BINARY_NAME=papercast

all: test build

build:
	go build -ldflags="-s -w" -o $(BINARY_NAME) .

test:
	go test -v ./...

install: build
	install -m 755 $(BINARY_NAME) $(HOME)/.local/bin/

clean:
	rm -f $(BINARY_NAME) papercast-bin
