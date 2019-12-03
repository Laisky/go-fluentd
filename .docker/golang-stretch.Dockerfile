# docker build . -f ./.docker/golang-stretch.Dockerfile -t registry.paas.ptcloud.t.home/paas/golang:1.13.4-stretch
# docker push registry.paas.ptcloud.t.home/paas/golang:1.13.4-stretch
FROM golang:1.13.4-stretch

# run dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends g++ make gcc git build-essential ca-certificates curl && \
    update-ca-certificates
