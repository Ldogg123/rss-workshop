FROM --platform=$BUILDPLATFORM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/rss-workshop ./cmd/server \
    && mkdir -p /out/data \
    && dpkg-query -W -f='${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\n' ca-certificates > /out/debian-packages.tsv

FROM scratch
ARG VERSION=dev
ARG COMMIT=unknown
ARG SOURCE_URL=https://github.com/Ldogg123/rss-workshop
LABEL org.opencontainers.image.title="RSS Workshop" \
      org.opencontainers.image.description="Self-hosted website-to-RSS and Atom feeds; static and external FlareSolverr fetching" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.source="${SOURCE_URL}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/share/doc/ca-certificates /usr/share/doc/ca-certificates
COPY --from=build /usr/share/common-licenses /usr/share/common-licenses
COPY --from=build /out/debian-packages.tsv /licenses/debian-packages.tsv
COPY --from=build /out/rss-workshop /rss-workshop
COPY --from=build /src/LICENSE /licenses/RSS-Workshop-LICENSE
COPY --from=build /src/docs/licenses /licenses
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
WORKDIR /data
ENV DATA_DIR=/data LISTEN_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD ["/rss-workshop", "-healthcheck"]
ENTRYPOINT ["/rss-workshop"]
