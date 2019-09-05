# docker build . -f ./.docker/test.Dockerfile -t registry:5000/go-fluentd-test:v1
# docker push registry:5000/go-fluentd-test:v1
FROM registry:5000/gobase:1.13.0-alpine3.10
ENV GO111MODULE=on



WORKDIR /go-fluentd
COPY go.mod .
COPY go.sum .
RUN go mod download

ADD . .
CMD go test -coverprofile=coverage.txt -covermode=atomic ./...
