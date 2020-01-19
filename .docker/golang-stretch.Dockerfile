# docker build . -f ./.docker/golang-stretch.Dockerfile -t ppcelery/golang:1.13.6-stretch
# docker push ppcelery/golang:1.13.6-stretch
FROM golang:1.13.6-stretch

# run dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends g++ make gcc git build-essential ca-certificates curl && \
    update-ca-certificates
