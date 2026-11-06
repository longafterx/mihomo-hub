# syntax=docker/dockerfile:1.7
# 前端必须先由 scripts/build-frontend.sh 从锁定提交构建，镜像只接收静态产物。
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY build/frontend.lock.yaml ./build/frontend.lock.yaml
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=0.3.0
ARG BACKEND_COMMIT
ARG FRONTEND_COMMIT
ARG BUILD_TIME=unknown
RUN set -eu; \
    test -n "$BACKEND_COMMIT"; \
    test "$FRONTEND_COMMIT" = "$(sed -n 's/^ref: "\([a-f0-9]*\)"/\1/p' build/frontend.lock.yaml)"; \
    test -f internal/web/dist/index.html; \
    test ! -f internal/web/dist/sw.js; \
    grep -F "$FRONTEND_COMMIT" internal/web/dist/mihomohub-build.json; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -p 2 -trimpath -buildvcs=false -tags=frontend \
    -ldflags "-s -w -X github.com/longafterx/mihomo-hub/internal/app.Version=$VERSION -X github.com/longafterx/mihomo-hub/internal/app.Commit=$BACKEND_COMMIT -X github.com/longafterx/mihomo-hub/internal/app.FrontendCommit=$FRONTEND_COMMIT -X github.com/longafterx/mihomo-hub/internal/app.BuildTime=$BUILD_TIME" \
    -o /out/mihomo-hub ./cmd/mihomo-hub; \
    mkdir -p /out/data; chmod 0700 /out/data

FROM scratch
ARG VERSION=0.3.0
ARG BACKEND_COMMIT
ARG FRONTEND_COMMIT
LABEL org.opencontainers.image.title="MihomoHub" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$BACKEND_COMMIT \
      io.mihomohub.frontend-commit=$FRONTEND_COMMIT
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mihomo-hub /mihomo-hub
COPY --from=build --chown=65532:65532 /out/data /var/lib/mihomo-hub
COPY build/notices /usr/share/licenses/mihomo-hub
USER 65532:65532
WORKDIR /var/lib/mihomo-hub
ENV MIHOMOHUB_LISTEN=0.0.0.0:8080 \
    MIHOMOHUB_DATABASE_PATH=/var/lib/mihomo-hub/mihomo-hub.db \
    MIHOMOHUB_MASTER_KEY_FILE=/var/lib/mihomo-hub/secrets/master.key
VOLUME ["/var/lib/mihomo-hub"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD ["/mihomo-hub", "healthcheck"]
ENTRYPOINT ["/mihomo-hub"]
CMD ["serve"]
