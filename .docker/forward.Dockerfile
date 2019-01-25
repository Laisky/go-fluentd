# docker build . -f ./.docker/Dockerfile-forward -t registry:5000/go-fluentd-forward:666
# docker run -it --rm --cap-add SYS_ADMIN --device /dev/fuse -e MFS_MASTER=mfs-master.sit.ptcloud.t.home -e TZ=Asia/Shanghai -v /opt/configs/go-fluentd/forward:/forward registry:5000/go-fluentd-forward:666 /bin/sh
# sh startApp.sh
# cp /forward/settings.yml /data/Sit/go-fluentd/settings/.
FROM registry:5000/golang-stretch:1.11.5 AS gobin

# http proxy
ENV HTTP_PROXY=http://172.16.4.26:17777
ENV HTTPS_PROXY=http://172.16.4.26:17777

RUN mkdir -p /go/src/github.com/Laisky/go-fluentd
ADD . /go/src/github.com/Laisky/go-fluentd
WORKDIR /go/src/github.com/Laisky/go-fluentd

# install dependencies
RUN glide i

# static build
RUN go build --ldflags '-extldflags "-static"' entrypoints/main.go

# copy executable file and certs to a pure container
FROM registry:5000/mfs-stretch:20180116

COPY --from=gobin /etc/ssl/certs /etc/ssl/certs
COPY --from=gobin /go/src/github.com/Laisky/go-fluentd/main go-fluentd
ADD ./startApp.sh .

CMD ["sh", "startApp.sh"]
