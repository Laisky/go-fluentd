FROM registry:5000/gobase:1.12.1-alpine3.9 AS gobuild

ADD . /go-fluentd
WORKDIR /go-fluentd

# static build
RUN go mod download
RUN go build -a --ldflags '-extldflags "-static"' entrypoints/main.go


# copy executable file and certs to a pure container
FROM alpine:3.9
COPY --from=gobuild /etc/ssl/certs /etc/ssl/certs
COPY --from=gobuild /go-fluentd/main go-fluentd

CMD ["./go-fluentd", "--config=/etc/go-fluentd/settings"]
