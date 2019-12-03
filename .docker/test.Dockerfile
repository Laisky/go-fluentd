# docker build . -f ./.docker/test.Dockerfile -t registry.paas.ptcloud.t.home/paas/go-fluentd-test:v1
# docker push registry.paas.ptcloud.t.home/paas/go-fluentd-test:v1
FROM registry.paas.ptcloud.t.home/paas/gobase:1.13.4-alpine3.10
ENV GO111MODULE=on

WORKDIR /go-fluentd
COPY go.mod .
COPY go.sum .
RUN go mod download

ADD . .
CMD go test -coverprofile=coverage.txt -covermode=atomic ./...
