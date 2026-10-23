# syntax=docker/dockerfile:1

# Build stage: full Go toolchain, thrown away after the build.
FROM golang:1.23 AS build
WORKDIR /src

# Download modules first so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# CMD selects which binary to build: api (default) or worker.
ARG CMD=api
# CGO_ENABLED=0: a fully static binary that needs no libc at runtime.
# -trimpath drops local paths; -s -w strips the symbol table and DWARF data.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

# Runtime stage: CA certificates, tzdata and the binary. No shell, no package
# manager, runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]
