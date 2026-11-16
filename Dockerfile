# syntax=docker/dockerfile:1.7
# xueer-speaker: the Go service plus the edge-tts command it uses for the
# voice. docker compose up -d (see compose.yaml), or build alone with:
#   docker build -t xueer-speaker .
ARG GO_IMAGE=golang:1.25-alpine
ARG BASE_IMAGE=python:3.12-slim-bookworm

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG VERSION=dev
ARG TARGETOS TARGETARCH
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd cmd
COPY internal internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags timetzdata -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/xueer-speaker ./cmd/xueer-speaker

FROM ${BASE_IMAGE}
RUN pip install --no-cache-dir --no-compile edge-tts==7.3.1 \
 && groupadd -g 10003 speaker && useradd -u 10003 -g 10003 -M -d /data -s /usr/sbin/nologin speaker \
 && mkdir -p /data && chown speaker:speaker /data
COPY --from=build /out/xueer-speaker /usr/local/bin/xueer-speaker
ENV TZ=Asia/Shanghai XS_DATA=/data XS_INTERNAL=:8101
USER 10003:10003
WORKDIR /data
VOLUME /data
EXPOSE 8100 8101 8102
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/usr/local/bin/xueer-speaker", "check"]
ENTRYPOINT ["/usr/local/bin/xueer-speaker"]
CMD ["serve"]
