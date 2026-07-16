# TikTelemetry — multi-arch Docker image
#
# Build with buildx for multiple platforms:
#   docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v6 \
#     -t youruser/tiktelemetry:latest --push .
#
# The arm target is built GOARM=6 (see the build stage below) for MikroTik
# compatibility, so it is published as linux/arm/v6.

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

# GOARM for 32-bit ARM: we deliberately target GOARM=6, NOT the buildx
# TARGETVARIANT (v7). GOARM=7 emits VFPv3 float instructions that several
# MikroTik ARM SoCs do not implement, so the binary crashes on the router with
# "exited with signal 4 (Illegal instruction)". GOARM=6 (VFPv1/v2) is a strict
# subset of VFPv3, runs on every MikroTik ARM container host (except the ARMv5
# EN7562CT / hEX Refresh, which needs a separate arm/v5 build), and costs
# nothing measurable for this I/O-bound agent. RouterOS also ignores the OCI
# `variant` field when selecting the `arm` image, so the single arm build must
# itself be broadly compatible. Non-arm targets ignore GOARM.
RUN case "${TARGETVARIANT}" in \
      v5) GOARM=5 ;; \
      *)  GOARM=6 ;; \
    esac; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${GOARM} \
    go build -ldflags="-s -w -X main.version=${VERSION}" -o /tiktelemetry ./cmd/tiktelemetry

# ── Stage 2: Scratch runtime ────────────────────────────────────────────────────
FROM scratch

# CA certificates for HTTPS OTLP connections
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /tiktelemetry /tiktelemetry

ENV GOMEMLIMIT=24MiB

EXPOSE 8728 8729

ENTRYPOINT ["/tiktelemetry"]
