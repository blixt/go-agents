FROM golang:1.25.6-bookworm

ARG WATCHEXEC_VERSION=1.24.1
ARG TARGETARCH

RUN apt-get update \
  && apt-get install -y --no-install-recommends curl ca-certificates git xz-utils unzip \
  && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
  arch="${TARGETARCH:-amd64}"; \
  case "$arch" in \
    amd64) arch="x86_64" ;; \
    arm64) arch="aarch64" ;; \
    *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
  esac; \
  for ext in tar.xz tar.gz; do \
    url="https://github.com/watchexec/watchexec/releases/download/v${WATCHEXEC_VERSION}/watchexec-${WATCHEXEC_VERSION}-${arch}-unknown-linux-gnu.${ext}"; \
    if curl -fsSL "$url" -o "/tmp/watchexec.${ext}"; then \
      tar -C /tmp -xf "/tmp/watchexec.${ext}"; \
      break; \
    fi; \
  done; \
  mv /tmp/watchexec-*/watchexec /usr/local/bin/watchexec; \
  chmod +x /usr/local/bin/watchexec; \
  rm -rf /tmp/watchexec*;

RUN curl -fsSL https://bun.sh/install | bash
ENV PATH="/root/.bun/bin:${PATH}"
ENV GOTOOLCHAIN=auto

WORKDIR /app
