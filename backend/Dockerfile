FROM golang:1.22-alpine AS builder

WORKDIR /app

# We will install dependencies
COPY go.mod ./
# COPY go.sum ./
RUN go mod download || true

COPY . .
RUN go mod tidy
RUN go build -o balacer ./cmd/server/main.go
RUN go build -o balacer-cli ./cmd/cli/main.go

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/balacer .
COPY --from=builder /app/balacer-cli .
EXPOSE 80 443 443/udp 8080

CMD ["./balacer"]
