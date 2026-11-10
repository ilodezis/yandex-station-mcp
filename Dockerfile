# Builder: static Go binary
FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
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
FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
WORKDIR /app
COPY --from=build /out/station-mcp /app/station-mcp
ENV ADDR=:8150
USER 1000:1000
EXPOSE 8150
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/station-mcp", "health"]
ENTRYPOINT ["/app/station-mcp"]
