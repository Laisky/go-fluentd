# docker build -f .docker/Dockerfile -t go-fluentd .
FROM golang:1.27.1-bookworm AS gobuild

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags='-s -w' -o /out/go-fluentd .

FROM debian:bookworm-slim
COPY --from=gobuild /etc/ssl/certs /etc/ssl/certs
COPY --from=gobuild /out/go-fluentd /go-fluentd
ENTRYPOINT ["/go-fluentd"]
CMD ["--config=/etc/go-fluentd/settings.yml"]
