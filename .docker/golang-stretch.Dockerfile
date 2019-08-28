# docker build . -f ./.docker/golang-stretch.Dockerfile -t registry:5000/golang:1.12.9-stretch
# docker push registry:5000/golang:1.12.9-stretch
FROM golang:1.12.9-stretch

# run dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends g++ make gcc git build-essential ca-certificates curl && \
    update-ca-certificates
