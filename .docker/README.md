# Docker Containers

there are two kinds of runner container:

- go-fluentd
- go-fluentd-forward

![relations](./dockerfile-relations.jpg)


## go-fluentd

build on machine that should installed docker & golang.

```sh
go mod download
go mod vendor

# build base image
docker build . -f ./.docker/gobase.Dockerfile -t registry:5000/gobase:1.12.1-alpine3.9
docker push registry:5000/gobase:1.12.1-alpine3.9

# build image
docker build . -f ./.docker/Dockerfile -t registry:5000/go-fluentd:1.8.2
docker push registry:5000/go-fluentd:1.8.2

docker run -it --rm \
    --net=host \
    -v /opt/configs/go-fluentd:/etc/go-fluentd \
    -v /data/log/fluentd/go-concator:/data/log/fluentd/go-concator \
    registry:5000/go-fluentd:1.8.2 \
    ./go-fluentd --config=/etc/go-fluentd --env=prod --addr=0.0.0.0:22800 --log-level=error
```

## go-fluentd-foward

build on machine that should installed docker.

```sh
# build golang-stretch
docker build . -f ./.docker/golang-stretch.Dockerfile -t registry:5000/golang:1.12.1-stretch
docker push registry:5000/golang:1.12.1-stretch

# build mfs-stretch
docker build . -f ./.docker/mfs-stretch.Dockerfile -t registry:5000/mfs-stretch:20190116
docker push registry:5000/mfs-stretch:20190116

# build go-fluentd-forward
docker build . -f ./.docker/forward.Dockerfile -t registry:5000/go-fluentd-forward:666

docker run -it --rm \
    --cap-add SYS_ADMIN \
    --device /dev/fuse \
    --env MFS_MASTER=mfs-master.sit.ptcloud.t.home \
    --env TZ=Asia/Shanghai \
    -v /opt/configs/go-fluentd/forward:/forward \
    registry:5000/go-fluentd-forward:666
```
