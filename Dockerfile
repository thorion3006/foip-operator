# Build both controller binaries
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o foip           ./cmd/foip/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o node-interface  ./cmd/node-interface/

# Minimal runtime image with both binaries.
# The Deployment runs /foip; the DaemonSet runs /node-interface (set via pod spec command).
FROM gcr.io/distroless/static:nonroot
LABEL org.opencontainers.image.source="https://github.com/niklasbeierl/foip-operator"
WORKDIR /
COPY --from=builder /workspace/foip           /foip
COPY --from=builder /workspace/node-interface /node-interface
USER 65532:65532
