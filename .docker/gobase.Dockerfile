# docker build . -f ./.docker/gobase.Dockerfile -t ppcelery/gobase:1.14.0-alpine3.11
# docker push ppcelery/gobase:1.14.0-alpine3.11
FROM golang:1.14.0-alpine3.11

# run dependencies
RUN apk update && apk upgrade && \
    apk add --no-cache gcc git build-base ca-certificates curl && \
    update-ca-certificates
