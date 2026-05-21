FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /aiops-agent \
    ./cmd/aiops-agent

FROM alpine:3.21 AS helm-dl
ARG HELM_VERSION=v3.17.3
RUN apk add --no-cache curl tar && \
    curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz" | \
    tar -xz --strip-components=1 -C /tmp linux-amd64/helm

FROM alpine:3.21
RUN apk add --no-cache git ca-certificates && \
    addgroup -g 65532 nonroot && \
    adduser -u 65532 -G nonroot -S -D nonroot
COPY --from=builder /aiops-agent /aiops-agent
COPY --from=helm-dl /tmp/helm /usr/local/bin/helm
USER nonroot
ENTRYPOINT ["/aiops-agent"]
