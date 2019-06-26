# docker build . -f ./.docker/forward.Dockerfile -t registry:5000/go-fluentd-forward:666
# docker run -it --rm --cap-add SYS_ADMIN --device /dev/fuse -e MFS_MASTER=mfs-master.sit.ptcloud.t.home -e TZ=Asia/Shanghai -v /opt/configs/go-fluentd/forward:/forward registry:5000/go-fluentd-forward:666 /bin/sh
# sh startApp.sh
# cp /forward/settings.yml /data/Sit/go-fluentd/settings/.
FROM registry:5000/golang:1.12.6-stretch AS gobin

ENV GO111MODULE=on
WORKDIR /go-fluentd
COPY go.mod .
COPY go.sum .
RUN go mod download

# static build
ADD . .
RUN go build -a --ldflags '-extldflags "-static"' entrypoints/main.go

# copy executable file and certs to a pure container
FROM registry:5000/mfs-stretch:20190116

COPY --from=gobin /etc/ssl/certs /etc/ssl/certs
COPY --from=gobin /go-fluentd/main go-fluentd
ADD ./startApp.sh .

CMD ["sh", "startApp.sh"]
