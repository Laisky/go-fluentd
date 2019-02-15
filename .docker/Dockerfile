FROM golang:1.11.5-alpine3.9 AS gobin

# http proxy
ENV HTTP_PROXY=http://172.16.4.26:17777
ENV HTTPS_PROXY=http://172.16.4.26:17777

# run dependencies
RUN apk update && apk upgrade && \
    apk add --no-cache gcc git build-base ca-certificates && \
    update-ca-certificates
RUN mkdir -p /go/src/github.com/Laisky/go-fluentd
ADD . /go/src/github.com/Laisky/go-fluentd
WORKDIR /go/src/github.com/Laisky/go-fluentd

# static build
RUN go build --ldflags '-extldflags "-static"' entrypoints/main.go

# copy executable file and certs to a pure container
FROM alpine:3.9
COPY --from=gobin /etc/ssl/certs /etc/ssl/certs
COPY --from=gobin /go/src/github.com/Laisky/go-fluentd/main go-fluentd

CMD ["./go-fluentd", "--config=/etc/go-fluentd/settings"]
