# Build stage: static binaries, no cgo needed.
FROM golang:1.22 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vireo . \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vireo-chat ./cmd/vireo-chat \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fakeagent ./cmd/fakeagent

# Runtime stage: distroless has no shell, no package manager, runs as nonroot.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/vireo /out/vireo-chat /out/fakeagent /app/
COPY certs/ /app/certs/
COPY config/ /app/config/

USER nonroot
EXPOSE 7777/tcp
EXPOSE 7777/udp

ENTRYPOINT ["/app/vireo"]
CMD ["--config", "demo"]
