# docker build . -f ./.docker/test.Dockerfile -t registry:5000/go-fluentd-test:v1
# docker push registry:5000/go-fluentd-test:v1
FROM registry:5000/gobase:1.12.1-alpine3.9

# http proxy
ENV HTTP_PROXY=http://172.16.4.26:17777
ENV HTTPS_PROXY=http://172.16.4.26:17777

ADD . /go-fluentd
WORKDIR /go-fluentd

RUN go mod download

CMD go test -cover ./...
