# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags="-s -w" -o /out/journalol ./cmd/journalol

FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S journalol \
    && adduser -S -G journalol -h /app journalol \
    && mkdir -p /app /data \
    && chown -R journalol:journalol /app /data

WORKDIR /app

COPY --from=build --chown=journalol:journalol /out/journalol /app/journalol

USER journalol

EXPOSE 8080

ENTRYPOINT ["/app/journalol"]
