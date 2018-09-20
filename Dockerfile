FROM golang:1.11.0-alpine3.8 AS gobin
RUN apk update && apk upgrade && \
    apk add --no-cache g++ make gcc git ca-certificates && \
    update-ca-certificates
RUN mkdir -p /go/src/github.com/Laisky/go-concator
ADD . /go/src/github.com/Laisky/go-concator
WORKDIR /go/src/github.com/Laisky/go-concator
RUN go build --ldflags '-extldflags "-static"' entrypoints/main.go

FROM alpine:3.8
COPY --from=gobin /etc/ssl/certs /etc/ssl/certs
COPY --from=gobin /go/src/github.com/Laisky/go-concator/main go-concator

CMD ["./go-concator", "--config=/etc/go-concator/settings"]
