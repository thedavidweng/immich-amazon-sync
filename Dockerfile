# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.version=$VERSION" \
    -o /out/immich-amazon-sync . \
    && mkdir /out/data && chmod 0700 /out/data

FROM alpine:3.24.3
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="immich-amazon-sync" \
      org.opencontainers.image.source="https://github.com/thedavidweng/immich-amazon-sync" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/immich-amazon-sync /usr/local/bin/immich-amazon-sync
COPY --from=build --chown=10001:10001 /out/data /data
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/licenses/immich-amazon-sync/
USER 10001:10001
ENV DATA_DIR=/data LISTEN=0.0.0.0:8080
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD wget -q -T 3 -O /dev/null http://127.0.0.1:8080/healthz \
     && wget -q -T 3 -O /dev/null http://127.0.0.1:8080/readyz
ENTRYPOINT ["/usr/local/bin/immich-amazon-sync"]
CMD ["serve"]
