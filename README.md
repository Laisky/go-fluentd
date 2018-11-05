# Go-Concator

Rewrite fluentd by Golang.


## Description

Replace most of fluentd's functions except string parsing
(since of this part is too cumbersome to rewrite
 and can be easy to horizontal scaling).


## Roles

- Acceptor (consists of Recvs)
- AcceptorPipeline (consists of AcceptorFilters)
- Journal
- Dispatcher
- DispatcherPipeline (consists of DispatcherFilters)
- Concator
- Producer


![architecture](docs/architecture.jpg)


### Acceptor

Contains multiply Recvs (such as KafkRecv & TcpRecv),
can listening tcp port or fetch msg from kafka brokers.


### AcceptorPipeline

Contains multiply AcceptorFilters, be used for ignore or retag specific messages.
All filters should return very fast to avoid blocking.


### Journal

TBD...


### Dispatcher

TBD...


### DispatcherPipeline

TBD...


### Concator

TBD...


### Producer

TBD...


