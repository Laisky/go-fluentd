# Docker Containers

there are two kinds of runner container:

- go-fluentd
- go-fluentd-forward

![relations](./dockerfile-relations.jpg)


## go-fluentd

build on machine that should installed docker & golang.

```sh
# build image
docker build . -f ./.docker/Dockerfile -t ppcelery/go-fluentd:1.12.7
docker push ppcelery/go-fluentd:1.12.7

docker run -it --rm \
    --net=host \
    -v /opt/configs/go-fluentd:/etc/go-fluentd \
    -v /data/log/fluentd/go-concator:/data/log/fluentd/go-concator \
    ppcelery/go-fluentd:1.12.7 \
    ./go-fluentd --config=/etc/go-fluentd/ --env=prod --addr=0.0.0.0:22800 --log-level=error
```

## go-fluentd-foward

build on machine that should installed docker.

```sh
# build golang-stretch
docker build . -f ./.docker/golang-stretch.Dockerfile -t ppcelery/golang:1.13.6-stretch
docker push ppcelery/golang:1.13.6-stretch

# build mfs-stretch
docker build . -f ./.docker/mfs-stretch.Dockerfile -t ppcelery/mfs-stretch:20190116
docker push ppcelery/mfs-stretch:20190116

# build go-fluentd-forward
docker build . -f ./.docker/forward.Dockerfile -t ppcelery/go-fluentd-forward:666

docker run -it --rm \
    --cap-add SYS_ADMIN \
    --device /dev/fuse \
    --env MFS_MASTER=mfs-master.sit.ptcloud.t.home \
    --env TZ=Asia/Shanghai \
    -v /opt/configs/go-fluentd/forward:/forward \
    ppcelery/go-fluentd-forward:666
```
