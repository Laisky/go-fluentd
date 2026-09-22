# Historical filename retained for callers; the Go builder now uses Bookworm.
# docker build -f .docker/golang-stretch.Dockerfile -t go-fluentd-gobase:1.27.1 .
FROM golang:1.27.1-bookworm
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
