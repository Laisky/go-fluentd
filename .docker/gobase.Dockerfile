# docker build . -f ./.docker/gobase.Dockerfile -t registry:5000/gobase:1.13.3-alpine3.10
# docker push registry:5000/gobase:1.13.3-alpine3.10
FROM golang:1.13.3-alpine3.10

# run dependencies
RUN apk update && apk upgrade && \
    apk add --no-cache gcc git build-base ca-certificates curl && \
    update-ca-certificates
