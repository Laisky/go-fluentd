# docker build --build-arg http_proxy=http://172.16.4.26:17777 --build-arg https_proxy=http://172.16.4.26:17777
FROM ppcelery/gobase:1.13.6-alpine3.11 AS gobuild

ENV GO111MODULE=on
WORKDIR /go-fluentd
COPY go.mod .
COPY go.sum .
RUN go mod download

# static build
ADD . .
RUN go build -a --ldflags '-extldflags "-static"' entrypoints/main.go


# copy executable file and certs to a pure container
FROM alpine:3.11
COPY --from=gobuild /etc/ssl/certs /etc/ssl/certs
COPY --from=gobuild /go-fluentd/main go-fluentd

CMD ["./go-fluentd", "--config=/etc/go-fluentd/settings"]
