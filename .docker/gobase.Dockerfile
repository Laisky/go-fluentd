# docker build . -f ./.docker/gobase.Dockerfile -t registry:5000/gobase:1.12.1-alpine3.9
# docker push registry:5000/gobase:1.12.1-alpine3.9
FROM golang:1.12.1-alpine3.9

# http proxy
ENV HTTP_PROXY=http://172.16.4.26:17777
ENV HTTPS_PROXY=http://172.16.4.26:17777

# run dependencies
RUN apk update && apk upgrade && \
    apk add --no-cache gcc git build-base ca-certificates curl && \
    update-ca-certificates

# glide install go dependencies
RUN curl https://glide.sh/get | sh

CMD glide -v
