# SPDX-License-Identifier: Apache-2.0
# Triage server image: CGO build, ONNX Runtime bundled (dlopen at runtime), distroless/cc, non-root.
FROM golang:1.27-bookworm@sha256:21ade014faf952365b9ecbd8dbdb87176fbacb82fac9fb132d9f2a7b8cc3f132 AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETARCH
# ORT_URL / ORT_SHA256 come from models/manifest.yaml (`onnxruntime:` key) via scripts/docker.sh.
ARG ORT_URL=""
ARG ORT_SHA256=""
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=1
RUN go build -trimpath \
      -ldflags "-s -w -X github.com/thnhdatle/triage/internal/platform/buildinfo.Version=${VERSION} -X github.com/thnhdatle/triage/internal/platform/buildinfo.Commit=${COMMIT}" \
      -o /out/triage ./cmd/triage
RUN mkdir -p /out/ort && \
    if [ -n "${ORT_URL}" ]; then \
      curl -fsSL -o /tmp/ort.tgz "${ORT_URL}" && \
      echo "${ORT_SHA256}  /tmp/ort.tgz" | sha256sum -c - && \
      mkdir /tmp/ort && tar -xzf /tmp/ort.tgz -C /tmp/ort --strip-components=1 && \
      cp -L /tmp/ort/lib/libonnxruntime.so* /out/ort/ && \
      cp /tmp/ort/LICENSE /out/ort/LICENSE && \
      cp /tmp/ort/ThirdPartyNotices.txt /out/ort/ThirdPartyNotices.txt; \
    else \
      echo "no onnxruntime pin in models/manifest.yaml; image has no ORT (bert/NER need a mounted library)" > /out/ort/README; \
    fi

FROM gcr.io/distroless/cc-debian12:nonroot@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f
COPY --from=build /out/triage /triage
COPY --from=build /out/ort/ /opt/onnxruntime/
ENV TRIAGE_ONNX_LIBRARY_PATH=/opt/onnxruntime/libonnxruntime.so
USER 65532:65532
EXPOSE 8080 9091
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/triage","healthcheck"]
ENTRYPOINT ["/triage"]
CMD ["serve","--config","/etc/triage/config.yaml"]
