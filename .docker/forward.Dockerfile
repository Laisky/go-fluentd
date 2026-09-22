# Legacy MooseFS integration: the runtime image and startApp.sh are deployment-owned.
# CI validates the Go build stage without requiring that external runtime.
# docker build --target gobin -f .docker/forward.Dockerfile -t go-fluentd-forward-build .
FROM golang:1.27.1-bookworm AS gobin

WORKDIR /go-fluentd
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags='-s -w' -o main .

FROM ppcelery/mfs-stretch:20190116
COPY --from=gobin /etc/ssl/certs /etc/ssl/certs
COPY --from=gobin /go-fluentd/main /go-fluentd
COPY startApp.sh /startApp.sh
CMD ["sh", "/startApp.sh"]
