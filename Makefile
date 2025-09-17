PROTOC_GEN := $(shell go env GOPATH)/bin/protoc-gen-go
PROTOC_GEN_GRPC := $(shell go env GOPATH)/bin/protoc-gen-go-grpc

.PHONY: deps
deps:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.4.0

.PHONY: proto
proto:
	protoc \
		-I proto \
		--go_out=paths=source_relative:./pb \
		--go-grpc_out=paths=source_relative:./pb \
		proto/balancer/v1/balancer.proto \
		proto/captcha/v1/captcha.proto

.PHONY: build
build:
	go build ./...

.PHONY: run
run:
	go run ./cmd/captcha-service


