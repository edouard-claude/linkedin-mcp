# --- build ---
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/linkedin-mcp ./cmd/linkedin-mcp

# The data directory is created here, owned by the unprivileged uid the final
# image runs as. Docker copies that ownership when it initialises a fresh named
# volume on the same path, which is what lets the process write its SQLite file
# without running as root.
RUN mkdir -p /data && chown 65532:65532 /data

# --- run ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/linkedin-mcp /linkedin-mcp
COPY --from=build --chown=65532:65532 /data /data
VOLUME ["/data"]
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/linkedin-mcp"]
