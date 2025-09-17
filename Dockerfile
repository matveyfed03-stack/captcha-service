FROM golang:1.22 as build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /captcha-service ./cmd/captcha-service

FROM gcr.io/distroless/base-debian12
COPY --from=build /captcha-service /captcha-service
ENV MIN_PORT=38000 MAX_PORT=40000 MAX_SHUTDOWN_INTERVAL=600 METRICS_ADDR=":9090"
EXPOSE 38000-40000 9090
ENTRYPOINT ["/captcha-service"]


