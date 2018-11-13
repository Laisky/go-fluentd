# Go-Concator

Rewrite fluentd by Golang.


## Description

Replace most of fluentd's functions except string parsing
(since of this part is too cumbersome to rewrite
 and can be easy to horizontal scaling).


 ## Run

build:

```sh
docker build . -t ppcelery/go-concator:latest
```

run:

```sh
docker run -itd --rm --name=go-concator -p 24225:24225 -p 8080:8080 \
    -v /opt/configs/go-concator:/etc/go-concator \
    -v /data/log/fluentd/go-concator:/data/log/fluentd/go-concator
    ppcelery/go-concator:latest \
        --config=/etc/go-concator \
        --env=perf \
        --addr=0.0.0.0:8080
```


### docker images version

- stable
- release
- dev
- <feature task id>


## Roles

- Acceptor (consists of Recvs)
- AcceptorPipeline (consists of AcceptorFilters)
- Journal
- Dispatcher
- TagPipeline (consists of TagFilters)
    - Concator
    - Parser for each tag
- PostPipeline (consists of PostFilters)
- Producer


![architecture](docs/architecture.jpg)


### Acceptor

Contains multiply Recvs (such as KafkRecv & TcpRecv),
can listening tcp port or fetch msg from kafka brokers.


### AcceptorPipeline

Contains multiply AcceptorFilters, be used for ignore or retag specific messages.
All filters should return very fast to avoid blocking.


### Journal

...


### Dispatcher

...


### TagPipeline

...


### PostPipeline

...


### Producer

...


