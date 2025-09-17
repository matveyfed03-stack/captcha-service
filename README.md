## Captcha Service (gRPC)

A reference implementation per the provided spec. Exposes gRPC `CaptchaService` and registers itself to a balancer with heartbeats.

### Features
- Env-based free port selection `[MIN_PORT, MAX_PORT]` (defaults 38000-40000)
- `NewChallenge`: returns inline HTML for iframe with `window.postMessage`
- `MakeEventStream`: bidi stream; returns success on first frontend event
- Balancer `RegisterInstance` heartbeats every second
- Graceful shutdown via `MAX_SHUTDOWN_INTERVAL` (default 600s)

### Requirements
- Go 1.22+
- `protoc` compiler

### Setup
```powershell
cd D:\GoProjects\captcha-service
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\gen-proto.ps1 -Install
go mod tidy
```

### Run
Env vars:
- `MIN_PORT`, `MAX_PORT`
- `MAX_SHUTDOWN_INTERVAL`
- `BALANCER_ADDR` (optional `host:port`)
- `CHALLENGE_TYPE` (default `basic-click`)

Run without balancer:
```powershell
$env:MIN_PORT = 38000
$env:MAX_PORT = 38100
go run .\cmd\captcha-service
```

With balancer:
```powershell
$env:BALANCER_ADDR = "127.0.0.1:39000"
$env:CHALLENGE_TYPE = "basic-click"
go run .\cmd\captcha-service
```



