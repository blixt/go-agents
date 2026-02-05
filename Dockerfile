FROM golang:1.25.6-bookworm

ARG TARGETARCH

RUN apt-get update \
  && apt-get install -y --no-install-recommends curl ca-certificates git xz-utils unzip \
  && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://bun.sh/install | bash
ENV PATH="/root/.bun/bin:${PATH}"
ENV GOTOOLCHAIN=auto

WORKDIR /app

COPY go-llms ./go-llms
COPY go-agents ./go-agents

WORKDIR /app/go-agents

RUN go mod download
RUN go build -o /usr/local/bin/agentd ./cmd/agentd
