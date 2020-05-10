# docker build --build-arg http_proxy=http://172.16.4.26:17777 --build-arg https_proxy=http://172.16.4.26:17777
FROM golang:1.14.2-buster AS gobuild

# install dependencies
RUN apt-get update \
    && apt-get install -y --no-install-recommends g++ make gcc git build-essential ca-certificates curl \
    && update-ca-certificates

ENV GO111MODULE=on
WORKDIR /go-fluentd
COPY go.mod .
COPY go.sum .
RUN go mod download

# static build
ADD . .
# RUN CGO_ENABLED=0 go build -a -ldflags '-w -extldflags "-static"' entrypoints/main.go
RUN go build -a -ldflags '-w -extldflags "-static"' entrypoints/main.go


# copy executable file and certs to a pure container
FROM debian:buster
COPY --from=gobuild /etc/ssl/certs /etc/ssl/certs
COPY --from=gobuild /go-fluentd/main go-fluentd

ENTRYPOINT [ "./go-fluentd" ]
CMD ["--config=/etc/go-fluentd/settings.yml"]
