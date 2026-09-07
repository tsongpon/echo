# syntax=docker/dockerfile:1

# ---- Build stage ----
# Pin the patch version so builds are reproducible; go.mod declares go 1.26.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Cache dependencies first so source edits don't invalidate the module layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

# CGO disabled so the binary runs on the distroless/static base (no libc).
# -trimpath strips local paths; -s -w strips debug symbols for a smaller image.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/echo-api ./cmd/server

# ---- Runtime stage ----
# distroless/static has no shell, no package manager, and no libc, which
# minimizes attack surface. The app only needs ca-certificates (Firestore
# uses HTTPS) and static timezone data.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/echo-api /echo-api

# Cloud Run injects PORT=8080; the app reads it at startup.
ENV PORT=8080
EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/echo-api"]