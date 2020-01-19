# docker build . -f ./.docker/test.Dockerfile -t ppcelery/go-fluentd-test:v1
# docker push ppcelery/go-fluentd-test:v1
FROM ppcelery/gobase:1.13.6-alpine3.11
ENV GO111MODULE=on

WORKDIR /go-fluentd
COPY go.mod .
COPY go.sum .
RUN go mod download

ADD . .
CMD go test -coverprofile=coverage.txt -covermode=atomic ./...
