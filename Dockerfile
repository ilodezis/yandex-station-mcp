# Builder: static Go binary
FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal ./internal
ARG GIT_SHA=dev
ARG BUILD_DATE=unknown
RUN go build -trimpath \
    -ldflags="-s -w -X main.gitCommit=${GIT_SHA} -X main.buildDate=${BUILD_DATE}" \
    -o /out/station-mcp .

# Runtime: static binary only (time zones are embedded, SSH is in-process)
FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
WORKDIR /app
COPY --from=build /out/station-mcp /app/station-mcp
ENV ADDR=:8150
USER 1000:1000
EXPOSE 8150
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/station-mcp", "health"]
ENTRYPOINT ["/app/station-mcp"]
