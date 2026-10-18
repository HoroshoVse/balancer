# Pure-Go build (no CGO), cross-compiled on the native platform for each target
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o balancer ./cmd/server && \
    go build -trimpath -ldflags="-s -w" -o balancer-cli ./cmd/cli

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/balancer /app/balancer-cli ./
EXPOSE 80 443 443/udp 8080

CMD ["./balancer"]
