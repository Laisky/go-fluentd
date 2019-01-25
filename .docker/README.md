# Docker Containers

there are two kinds of runner container:

- go-fluentd
- go-fluentd-forward


## go-fluentd

build on machine that should installed docker & golang & glide.

```sh
glide i
docker build . -f ./.docker/Dockerfile -t registry:5000/go-fluentd:1.6.1
docker push registry:5000/go-fluentd:1.6.1

docker run -it --rm \
    --net=host \
    -v /opt/configs/go-fluentd:/etc/go-fluentd \
    -v /data/log/fluentd/go-concator:/data/log/fluentd/go-concator \
    registry:5000/go-fluentd:1.6.1 \
    ./go-fluentd --config=/etc/go-fluentd --env=prod --addr=0.0.0.0:22800 --log-level=error
```

## go-fluentd-foward

build on machine that should installed docker.

```sh
# build golang-stretch
docker build . -f ./.docker/golang-stretch.Dockerfile -t registry:5000/golang-stretch:1.11.5
docker push registry:5000/golang-stretch:1.11.5

# build mfs-stretch
docker build . -f ./.docker/mfs-stretch -t registry:5000/mfs-stretch:20180116
docker push registry:5000/mfs-stretch:20180116

# build go-fluentd-forward
docker build . -f ./.docker/Dockerfile-forward -t registry:5000/go-fluentd-forward:666

docker run -it --rm \
    --cap-add SYS_ADMIN \
    --device /dev/fuse \
    --env MFS_MASTER=mfs-master.sit.ptcloud.t.home \
    --env TZ=Asia/Shanghai \
    -v /opt/configs/go-fluentd/forward:/forward \
    registry:5000/go-fluentd-forward:666
```
