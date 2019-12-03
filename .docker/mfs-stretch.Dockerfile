# docker build . -f ./.docker/mfs-stretch.Dockerfile -t registry.paas.ptcloud.t.home/paas/mfs-stretch:20190116
# docker push registry.paas.ptcloud.t.home/paas/mfs-stretch:20190116
FROM debian:stretch

# mfs
RUN apt-get update && \
    apt-get install -y --no-install-recommends wget lsb-release fuse libfuse2 net-tools gnupg2
RUN wget -O - http://ppa.moosefs.com/moosefs.key | apt-key add -
RUN echo "deb http://ppa.moosefs.com/moosefs-3/apt/$(awk -F= '$1=="ID" { print $2 ;}' /etc/os-release)/$(lsb_release -sc) $(lsb_release -sc) main" > /etc/apt/sources.list.d/moosefs.list
RUN apt-get update && apt-get install -y --no-install-recommends moosefs-client && \
    rm -rf /var/lib/apt/lists/*
