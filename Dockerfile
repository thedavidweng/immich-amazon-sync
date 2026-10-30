# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM alpine:3.24.3 AS build
# mise.toml is the only Go compiler pin. Mirror the official golang:alpine
# build stage, but take the release from the project instead of an image tag.
# The alpine base already provides CA certificates and busybox wget/tar/sha256sum.
COPY mise.toml scripts/go-version.sh /tmp/
RUN --mount=type=cache,target=/go-download \
    set -eux; \
    go_version=$(sh /tmp/go-version.sh /tmp/mise.toml); \
    case "$(apk --print-arch)" in \
        x86_64) go_arch=amd64 ;; \
        aarch64) go_arch=arm64 ;; \
        *) printf 'unsupported build architecture: %s\n' "$(apk --print-arch)" >&2; exit 1 ;; \
    esac; \
    file="go${go_version}.linux-${go_arch}.tar.gz"; \
    wget -q -O "/go-download/$file.sha256" "https://dl.google.com/go/$file.sha256"; \
    if [ ! -f "/go-download/$file" ]; then \
        wget -q -O "/go-download/$file.part" "https://dl.google.com/go/$file"; \
        mv "/go-download/$file.part" "/go-download/$file"; \
    fi; \
    hash=$(awk '{print $1}' "/go-download/$file.sha256"); \
    (cd /go-download && echo "$hash *$file" | sha256sum -c -); \
    tar -C /usr/local -xzf "/go-download/$file"
ENV PATH=/usr/local/go/bin:$PATH \
    GOPATH=/go \
    GOTOOLCHAIN=local
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
