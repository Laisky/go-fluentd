# docker build . -f ./.docker/gobase.Dockerfile -t registry:5000/gobase:1.12.7-alpine3.10
# docker push registry:5000/gobase:1.12.7-alpine3.10
FROM golang:1.12.7-alpine3.10

# http proxy
ENV HTTP_PROXY=http://172.16.4.26:17777
ENV HTTPS_PROXY=http://172.16.4.26:17777

# run dependencies
RUN apk update && apk upgrade && \
    apk add --no-cache gcc git build-base ca-certificates curl && \
    update-ca-certificates
