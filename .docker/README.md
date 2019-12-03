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
docker build . -f ./.docker/gobase.Dockerfile -t registry.paas.ptcloud.t.home/paas/gobase:1.13.4-alpine3.10
docker push registry.paas.ptcloud.t.home/paas/gobase:1.13.4-alpine3.10

# build image
docker build . -f ./.docker/Dockerfile -t registry.paas.ptcloud.t.home/paas/go-fluentd:1.8.2
docker push registry.paas.ptcloud.t.home/paas/go-fluentd:1.8.2

docker run -it --rm \
    --net=host \
    -v /opt/configs/go-fluentd:/etc/go-fluentd \
    -v /data/log/fluentd/go-concator:/data/log/fluentd/go-concator \
    registry.paas.ptcloud.t.home/paas/go-fluentd:1.8.2 \
    ./go-fluentd --config=/etc/go-fluentd --env=prod --addr=0.0.0.0:22800 --log-level=error
```

## go-fluentd-foward

build on machine that should installed docker.

```sh
# build golang-stretch
docker build . -f ./.docker/golang-stretch.Dockerfile -t registry.paas.ptcloud.t.home/paas/golang:1.13.4-stretch
docker push registry.paas.ptcloud.t.home/paas/golang:1.13.4-stretch

# build mfs-stretch
docker build . -f ./.docker/mfs-stretch.Dockerfile -t registry.paas.ptcloud.t.home/paas/mfs-stretch:20190116
docker push registry.paas.ptcloud.t.home/paas/mfs-stretch:20190116

# build go-fluentd-forward
docker build . -f ./.docker/forward.Dockerfile -t registry.paas.ptcloud.t.home/paas/go-fluentd-forward:666

docker run -it --rm \
    --cap-add SYS_ADMIN \
    --device /dev/fuse \
    --env MFS_MASTER=mfs-master.sit.ptcloud.t.home \
    --env TZ=Asia/Shanghai \
    -v /opt/configs/go-fluentd/forward:/forward \
    registry.paas.ptcloud.t.home/paas/go-fluentd-forward:666
```
