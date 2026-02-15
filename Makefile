-include .env
export

.PHONY: build run test vet clean

BINARY = max-telegram-bridge-bot

build:
	CGO_ENABLED=1 go build -o $(BINARY) .

run: build
	./$(BINARY)

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY)
