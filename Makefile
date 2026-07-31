.PHONY: all proto test race vet

all: proto

proto:
	protoc --go_out=. --go-grpc_out=. proto/*.proto

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

.PHONY: coverage coverage-html

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

coverage-html:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html