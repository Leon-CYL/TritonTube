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
