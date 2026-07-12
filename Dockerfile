# Build both controller binaries
FROM docker.io/library/golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=unknown
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags="-s -w" -a -o foip ./cmd/foip/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags="-s -w" -a -o node-interface ./cmd/node-interface/

# Minimal runtime image with both binaries.
# The Deployment runs /foip; the DaemonSet runs /node-interface (set via pod spec command).
FROM scratch
ARG VERSION=unknown
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.source="https://github.com/thorion3006/foip-operator"
LABEL org.opencontainers.image.title="foip-operator"
LABEL org.opencontainers.image.description="Netcup failover IP operator"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${VCS_REF}"
LABEL org.opencontainers.image.created="${BUILD_DATE}"
LABEL org.opencontainers.image.licenses="Apache-2.0"
WORKDIR /
COPY --from=builder /workspace/foip           /foip
COPY --from=builder /workspace/node-interface /node-interface
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
