# Build stage: static binaries, no cgo needed.
FROM golang:1.22 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Discovered, not listed: every cmd/<name> and bridges/<name> with a main package,
# plus the root package as `vireo`. Adding a backend does not change this file.
RUN for d in cmd/*/ bridges/*/; do \
    [ -d "$d" ] || continue; \
    name="$(basename "$d")"; \
    if go list -f '{{.Name}}' "./$d" 2>/dev/null | grep -qx main; then \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "/out/$name" "./$d" || exit 1; \
    fi; \
    done && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vireo .

# Runtime stage: distroless has no shell, no package manager, runs as nonroot.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/ /app/
COPY certs/ /app/certs/
COPY config/ /app/config/

USER nonroot
EXPOSE 7777/tcp
EXPOSE 7777/udp

ENTRYPOINT ["/app/vireo"]
CMD ["--config", "demo"]
