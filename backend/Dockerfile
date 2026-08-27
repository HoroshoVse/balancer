FROM golang:1.22-alpine AS builder

WORKDIR /app

# We will install dependencies
COPY go.mod ./
# COPY go.sum ./
RUN go mod download || true

COPY . .
RUN go mod tidy
RUN go build -o balancer ./cmd/server/main.go
RUN go build -o balancer-cli ./cmd/cli/main.go

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/balancer .
COPY --from=builder /app/balancer-cli .
EXPOSE 80 443 443/udp 8080

CMD ["./balancer"]
