# docker build . -f ./.docker/gobase.Dockerfile -t registry.paas.ptcloud.t.home/paas/gobase:1.13.4-alpine3.10
# docker push registry.paas.ptcloud.t.home/paas/gobase:1.13.4-alpine3.10
FROM golang:1.13.4-alpine3.10

# run dependencies
RUN apk update && apk upgrade && \
    apk add --no-cache gcc git build-base ca-certificates curl && \
    update-ca-certificates
