param(
    [switch]$Install
)

if ($Install) {
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.4.0
}

$ErrorActionPreference = 'Stop'

protoc `
  -I proto `
  --go_out=paths=source_relative:./pb `
  --go-grpc_out=paths=source_relative:./pb `
  proto/balancer/v1/balancer.proto `
  proto/captcha/v1/captcha.proto

Write-Host "Protobufs generated."


