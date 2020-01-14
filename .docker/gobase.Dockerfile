# docker build . -f ./.docker/gobase.Dockerfile -t registry.paas.ptcloud.t.home/paas/gobase:1.13.6-alpine3.11
# docker push registry.paas.ptcloud.t.home/paas/gobase:1.13.6-alpine3.11
FROM golang:1.13.6-alpine3.11

# run dependencies
RUN apk update && apk upgrade && \
    apk add --no-cache gcc git build-base ca-certificates curl && \
    update-ca-certificates
