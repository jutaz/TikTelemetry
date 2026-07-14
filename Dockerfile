# TikTelemetry — multi-arch Docker image
#
# Build with buildx for multiple platforms:
#   docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 \
#     -t youruser/tiktelemetry:latest --push .

# ── Stage 1: Build ──────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS TARGETARCH TARGETVARIANT
# VERSION is stamped into the binary and reported by `tiktelemetry --version`.
ARG VERSION=dev

# TARGETVARIANT is e.g. "v7" for arm/v7; strip the leading "v" for GOARM.
# Harmlessly empty for non-arm architectures.
RUN VARIANT="${TARGETVARIANT#v}"; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${VARIANT} \
    go build -ldflags="-s -w -X main.version=${VERSION}" -o /tiktelemetry ./cmd/tiktelemetry

# ── Stage 2: Scratch runtime ────────────────────────────────────────────────────
FROM scratch

# CA certificates for HTTPS OTLP connections
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /tiktelemetry /tiktelemetry

ENV GOMEMLIMIT=24MiB

EXPOSE 8728 8729

ENTRYPOINT ["/tiktelemetry"]
