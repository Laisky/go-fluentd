# docker build . -f ./.docker/golang-stretch.Dockerfile -t registry:5000/golang:1.12-stretch
# docker push registry:5000/golang:1.12-stretch
FROM golang:1.12-stretch

# http proxy
ENV HTTP_PROXY=http://172.16.4.26:17777
ENV HTTPS_PROXY=http://172.16.4.26:17777

# run dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends g++ make gcc git build-essential ca-certificates curl && \
    update-ca-certificates

# glide install go dependencies
RUN curl https://glide.sh/get | sh
