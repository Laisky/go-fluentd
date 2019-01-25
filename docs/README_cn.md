# 1. Go-Fluentd 设计文档


<!-- TOC -->
- [1. Go-Fluentd 设计文档](#1-go-fluentd-%E8%AE%BE%E8%AE%A1%E6%96%87%E6%A1%A3)
  - [1.1. 概述](#11-%E6%A6%82%E8%BF%B0)
  - [1.2. 功能](#12-%E5%8A%9F%E8%83%BD)
    - [1.2.1. Acceptor & Recvs](#121-acceptor--recvs)
    - [1.2.2. AcceptorPipeline](#122-acceptorpipeline)
    - [1.2.3. Journal](#123-journal)
    - [1.2.4. Dispatcher & tagFilters](#124-dispatcher--tagfilters)
    - [1.2.5. PostPipeline](#125-postpipeline)
    - [1.2.6. Producer & Senders](#126-producer--senders)
    - [1.2.7. Controllor](#127-controllor)
    - [1.2.8. Monitor](#128-monitor)
  - [1.3. 使用](#13-%E4%BD%BF%E7%94%A8)
    - [1.3.1. 运行](#131-%E8%BF%90%E8%A1%8C)
    - [1.3.2. 配置](#132-%E9%85%8D%E7%BD%AE)

## 1.1. 概述

取代 Fluentd，实现高性能的 log aggregator, parser 等功能。

原因是因为 fluentd 无法利用多核，已经出现性能瓶颈。
而且我们目前的日志分化和解析逻辑非常多样化，开源组件已经无法满足需求，
所以需要自行开发更为灵活和高性能的日志解析组件。


使用语言：Golang v1.11.5


## 1.2. 功能

Go-Fluentd 采用插件化的设计，将日志从接收到转发的全部流程拆分为一个个不同的步骤，
每个步骤都可以按需的插入功能组件（只要实现了所需的接口方法）。

目前已经拆分的步骤有：

- 日志接收：acceptor + recvs
- 日志预处理：acceptorPipeline
- redo log：journal
- 日志解析：dispatcher + tagFilters
- 日志后处理：postPipeline
- 日志转发：producer + senders


除了步骤外，还有一些框架工具类：

- 监控：monitor
- 控制：controllor

![architect](https://s3.laisky.com/uploads/2019/01/go-fluentd-architecture.jpg)


需要强调的是，在整个流程中流转的都是 `*libs.Fluentd` 消息体（下文中以 msg 代称）：

```go
type FluentMsg struct {
    Tag     string
    Message map[string]interface{}
    Id      int64
    ExtIds  []int64
}
```

其中各个成员为：

- `Tag`：消息的 tag，将会决定消息如何在框架中流转；
- `Message`：承载的消息内容；
- `Id`：消息的唯一 id，用于 journal 中进行去重判断；
- `ExtIds`：当发生消息合并时，将被合并的消息的 id 保存其中。


不过，在 Message 映射表中，也会有几个保留字段用来保存元数据：

- `tag`：用来保存该消息真实的 tag（因为 msg.Tag 有时候会为了流转而被不断的改动）；
- `msgid`：等于 msg.Id，可用于对消息进行去重。


BTW：为了保证性能，所有的 msg 必须通过 sync.Pool 进行回收。


### 1.2.1. Acceptor & Recvs

Acceptor 是接收数据的框架。具体的接收方式是由 recvs 来实现的，任何 recv 只需要满足如下接口即可：

```go
type AcceptorRecvItf interface {
    SetSyncOutChan(chan<- *libs.FluentMsg)
    SetAsyncOutChan(chan<- *libs.FluentMsg)
    SetMsgPool(*sync.Pool)
    SetCounter(libs.CounterIft)
    Run()
    GetName() string
}
```

目前已经实现的 recvs 有：

- fluentd：以 tcp 端口的形式流式解析 fluentd 格式的数据；
- kafka：以 kafka consumer 的形式从 kafka brokers 拉取数据；
- rsyslog：以 udp/tcp 端口的形式流式解析 rsyslog 格式的数据；
- http：以 http post json 的形式接收日志；


recvs 负责将接收到的数据流转换为 *libs.Fluentd 类型后统一输出（需要确定 tag 和 id），acceptor 提供的输出 channel 有两个：

- `syncOutChan`：会被 journal 同步阻塞，主要用于 kafka 这类可以控速的来源；
- `asyncOutChan`：不会被阻塞，journal 阻塞后会跳过 journal，如果后续仍然堵塞就会丢弃消息，
  用于确保服务不会因为日志消费不及时受到影响；


### 1.2.2. AcceptorPipeline

AcceptorPipeline 负责对消息进行一些简单的前处理，会按顺序执行 acceptorFilters，
各个 filter 自行判断如何处理消息（跳过、处理或丢弃）。任何 filter 需要满足如下接口：

```go
type AcceptorFilterItf interface {
    SetUpstream(chan *libs.FluentMsg)
    SetMsgPool(*sync.Pool)

    Filter(*libs.FluentMsg) *libs.FluentMsg
    DiscardMsg(*libs.FluentMsg)
}
```

目前实现的 acceptorFilters 有：

- spring：重命名 tag，拆分为 cp、bot、app.spring 等；
- spark：过滤掉不合格式的日志（某些 spark 日志不进行记录）；
- default：过滤掉不支持的 tag。


各个 filters 返回 nil 的话，就会跳过后续的 filters。所以如果 filter 决定丢弃消息的话，需要自行处理回收。

需要注意的是，acceptorFilters 早于 journal，消息尚未被持久化到磁盘，
所以为了提高可靠性，不要在这里实现太复杂的逻辑，应该让 msg 尽可能快的通过。


### 1.2.3. Journal

Journal 扮演 redo log 的角色，将 AcceptorPipeline 后的所有消息都写入 data 文件，提供持久化。
任何被后续流程处理的消息，也需要将其 id 提交给 journal 并写入到 ids 文件中。

journal 会定期的重新读取历史的 data 和 ids 文件，首先加载所有的 ids，然后再逐一的读取 data 中的 msg，
如果 msg 的 id 已经存在于 ids 中，说明该消息已被处理，则跳过。
如果 msg 的 id 未存在于 ids 中，说明该消息还没有被后续流程处理，则将该 msg 传递给 journal dump，
也就是将其写入新的 data 文件，并进入后续流转。这样可以保证任何达到了 journal 的消息，至少会被消费一次，实现 At Least One 语义。

在程序中止时，只有 Acceptor 和 AccptorPipeline 内的消息和 Journal 内正在等待 dump 的消息会丢失，
所以 Acceptor 和 AccptorPipeline 的处理要尽可能的快，而 Journal dump 的目标磁盘的 IO 也要尽可能的高。
不过由于 Journal 使用的磁盘空间极小（视配置而定，一半小于 1 GB），而且全部采用顺序读写，所以应该能很好的利用页缓存，不会对磁盘带来太大压力。


### 1.2.4. Dispatcher & tagFilters

这是目前最主要的解析逻辑。其实最初导致 Fluentd 不能水平扩展的根本原因就在于我们需要让日志以 tag & container_id 来分流，
而目前使用的 lb 只支持 roundrobin 或 ip source，都不符合需求。
所以首先使用 dispatcher 按照 tag 对 msg 进行分流（channel），
每一个 channel 后都接上支持该 tag 的 tagfilters。最后再进行合流，以一个 channel 进行统一输出。

tagFilters 需要符合如下接口即可：

```go
type TagFilterFactoryItf interface {
    IsTagSupported(string) bool
    Spawn(string, chan<- *libs.FluentMsg) chan<- *libs.FluentMsg // Spawn(tag, outChan) inChan
    GetName() string

    SetMsgPool(*sync.Pool)
    SetCommittedChan(chan<- int64)
    SetDefaultIntervalChanSize(int)
    DiscardMsg(*libs.FluentMsg)
}
```

tagFilters 和 acceptorPipeline 以及 postPipeline 在设计上最大的区别在于：
Pipeline 是对每一个 msg，由外部去调用每一个 filters 的 Filter 方法，
而 tagFilters 是一系列 filters 通过 inChan 和 outChan 串联起来。
所以 tagFilters 中的每一个 filter 都需要自行决定是否要将 msg 传递给后续 channel，而且需要自行回收决定要丢弃的 msg。

目前已经实现的 tagFilters 有：

- concator：负责日志拼接，将被 docker 切分的日志拼为一整条（已 hardcoding 为第一个 tagfilter）；
- parser：日志解析，将字符串按照正则解析为结构化数据。


### 1.2.5. PostPipeline

负责对已解析的日志进行后处理，该步骤结束后就直接进入转发步骤。内部设计理念和 acceptorPipeline 完全一致，postFilters 的接口要求为：


```go
type PostFilterItf interface {
    SetUpstream(chan *libs.FluentMsg)
    SetMsgPool(*sync.Pool)
    SetCommittedChan(chan<- int64)

    Filter(*libs.FluentMsg) *libs.FluentMsg
    DiscardMsg(*libs.FluentMsg)
}
```

目前实现的 postFilters 有：

- elasticsearch_dispatcher: 按照 tag 分发到不同的 es_sender；
  - 不过后来发现单独为每个 tag 开一个 sender 似乎对 es 服务器的 CPU 压力更小，所以此功能后来没有启用。
- default：将所有的 []byte 类型转换为 string 类型，以方便后面序列化为可读的 json（hardcoding 为最后一个 postFilter）。


### 1.2.6. Producer & Senders

Producer 负责分发消息，支持多 senders，按照 msg.Tag 传递给 sender 进行发送，
既允许一个 sender 转发多个 tag，也支持多个 senders 转发同一个 tag。
senders 需要满足如下接口：

```go
type SenderItf interface {
    Spawn(string) chan<- *libs.FluentMsg // Spawn(tag) inChan
    IsTagSupported(string) bool
    DiscardWhenBlocked() bool
    GetName() string

    SetMsgPool(*sync.Pool)
    SetCommitChan(chan<- int64)
    SetSupportedTags([]string)
    SetDiscardChan(chan<- *libs.FluentMsg)
    SetDiscardWithoutCommitChan(chan<- *libs.FluentMsg)
}
```

producer 的主要职责除了将 msg 传递给各个 senders 外，还负责统计每个 sender 的发送状态，并择机回收 msg，回收的主要逻辑为：

- 首次获取到上游的 msg 后，会遍历 senders，在将 msg 传递给 senders 的同时，会记录有多少个 senders 支持该 tag（计为 n）；
- 每一个 sender 在发送成功或失败后，会将 msg.Id 交还给 producer（通过 discardChan 或 discardWithoutCommitChan）；
- producer 会对 senders 发回的 msg id 进行计数，当计数达到 n 时，认为该 msg 已经被所有 senders 处理，此时才回收该 msg。
  - 如果 msg id 全部是通过 discardChan 送回的，则在回收消息的同时，将 msg id 提交给 journal 进行记录；
  - 如果有任意 msg id 是通过 discardWithoutCommitChan 送回的，则仅回收消息，但是不向 journal 提交（这会导致该条消息在稍后会被重新处理）。


### 1.2.7. Controllor

这不是流程，而是代码的一个设计模式，在 controllor 内，对所有的流程组件进行初始化，并且进行拼接，最终实现完成的流程，可以理解为类似于 IoC 容器。


### 1.2.8. Monitor

一个基于 iris 提供 HTTP 监控接口的组件。通过 `HTTP GET /monitor` 返回监控结果。

在代码的任何地方，都可以通过调用 AddMetric 为最终的监控结果（以 json 呈现）中添加一个字段：

```go
func AddMetric(name string, metric func() map[string]interface{}) {
    metricGetter[name] = metric
}
```


其中的两个参数：

- `name`：会呈现为 json 中的一个 key；
- `metric`：一个返回 map 的 func，每当监控接口被调用时，该 func 就会被调用，并将结果拼合到最终的 json 之中。


一个监控结果的例子：

```go
{
  "acceptorPipeline": {
    "msgPerSec": 226.3
  },
  "controllor": {
    "goroutine": 467,
    "skipDumpChanCap": 150000,
    "skipDumpChanLen": 0,
    "waitAccepPipelineAsyncChanCap": 100000,
    "waitAccepPipelineAsyncChanLen": 0,
    "waitAccepPipelineSyncChanCap": 10000,
    "waitAccepPipelineSyncChanLen": 0,
    "waitCommitChanCap": 500000,
    "waitCommitChanLen": 0,
    "waitDispatchChanCap": 100000,
    "waitDispatchChanLen": 0,
    "waitDumpChanCap": 150000,
    "waitDumpChanLen": 0,
    "waitPostPipelineChanCap": 10000,
    "waitPostPipelineChanLen": 0,
    "waitProduceChanCap": 10000,
    "waitProduceChanLen": 0
  },
  "dispatcher": {
    "ai.sit.ChanCap": 10000,
    "ai.sit.ChanLen": 0,
    "ai.sit.MsgPerSec": 17.7,
    "app.spring.sit.ChanCap": 10000,
    "app.spring.sit.ChanLen": 0,
    "app.spring.sit.MsgPerSec": 6.1,
    "base.sit.ChanCap": 10000,
    "base.sit.ChanLen": 0,
    "base.sit.MsgPerSec": 79.2,
    "connector.sit.ChanCap": 10000,
    "connector.sit.ChanLen": 0,
    "connector.sit.MsgPerSec": 0.6,
    "cp.sit.ChanCap": 10000,
    "cp.sit.ChanLen": 0,
    "cp.sit.MsgPerSec": 0,
    "emqtt.sit.ChanCap": 10000,
    "emqtt.sit.ChanLen": 0,
    "emqtt.sit.MsgPerSec": 0,
    "geely.sit.ChanCap": 10000,
    "geely.sit.ChanLen": 0,
    "geely.sit.MsgPerSec": 9.6,
    "httpguard.sit.ChanCap": 10000,
    "httpguard.sit.ChanLen": 0,
    "httpguard.sit.MsgPerSec": 0,
    "msgPerSec": 220.2,
    "tsp.sit.ChanCap": 10000,
    "tsp.sit.ChanLen": 0,
    "tsp.sit.MsgPerSec": 107
  },
  "producer": {
    "ai.sit.GeneralESSender.ChanCap": 50000,
    "ai.sit.GeneralESSender.ChanLen": 0,
    "app.spring.sit.GeneralESSender.ChanCap": 50000,
    "app.spring.sit.GeneralESSender.ChanLen": 0,
    "base.sit.GeneralESSender.ChanCap": 50000,
    "base.sit.GeneralESSender.ChanLen": 0,
    "connector.sit.GeneralESSender.ChanCap": 50000,
    "connector.sit.GeneralESSender.ChanLen": 0,
    "cp.sit.GeneralESSender.ChanCap": 50000,
    "cp.sit.GeneralESSender.ChanLen": 0,
    "cp.sit.KafkaCpSender.ChanCap": 50000,
    "cp.sit.KafkaCpSender.ChanLen": 0,
    "discardChanCap": 50000,
    "discardChanLen": 0,
    "emqtt.sit.GeneralESSender.ChanCap": 50000,
    "emqtt.sit.GeneralESSender.ChanLen": 0,
    "geely.sit.GeelyESSender.ChanCap": 50000,
    "geely.sit.GeelyESSender.ChanLen": 0,
    "httpguard.sit.GeneralESSender.ChanCap": 50000,
    "httpguard.sit.GeneralESSender.ChanLen": 0,
    "msgPerSec": 16.6,
    "tsp.sit.GeneralESSender.ChanCap": 50000,
    "tsp.sit.GeneralESSender.ChanLen": 0,
    "waitToDiscardMsgNum": 1
  },
  "tagpipeline": {
    "ai.sit.concator_tagfilter.ChanCap": 10000,
    "ai.sit.concator_tagfilter.ChanLen": 0,
    "ai.sit.spring.ChanCap": 10000,
    "ai.sit.spring.ChanLen": 0,
    "app.spring.sit.concator_tagfilter.ChanCap": 10000,
    "app.spring.sit.concator_tagfilter.ChanLen": 0,
    "app.spring.sit.spring.ChanCap": 10000,
    "app.spring.sit.spring.ChanLen": 0,
    "base.sit.concator_tagfilter.ChanCap": 10000,
    "base.sit.concator_tagfilter.ChanLen": 0,
    "base.sit.spring.ChanCap": 10000,
    "base.sit.spring.ChanLen": 0,
    "connector.sit.concator_tagfilter.ChanCap": 10000,
    "connector.sit.concator_tagfilter.ChanLen": 0,
    "connector.sit.connector.ChanCap": 10000,
    "connector.sit.connector.ChanLen": 0,
    "cp.sit.concator_tagfilter.ChanCap": 10000,
    "cp.sit.concator_tagfilter.ChanLen": 0,
    "cp.sit.cp.ChanCap": 10000,
    "cp.sit.cp.ChanLen": 0,
    "emqtt.sit.emqtt.ChanCap": 10000,
    "emqtt.sit.emqtt.ChanLen": 0,
    "geely.sit.concator_tagfilter.ChanCap": 10000,
    "geely.sit.concator_tagfilter.ChanLen": 0,
    "geely.sit.geely.ChanCap": 10000,
    "geely.sit.geely.ChanLen": 0,
    "tsp.sit.concator_tagfilter.ChanCap": 10000,
    "tsp.sit.concator_tagfilter.ChanLen": 0,
    "tsp.sit.spring.ChanCap": 10000,
    "tsp.sit.spring.ChanLen": 0
  },
  "ts": "2018-12-26T06:12:01Z"
}
```


## 1.3. 使用

### 1.3.1. 运行

使用 glide 安装依赖，然后直接编译运行即可。

```sh
# 安装依赖
glide i

# 运行
➜  go-fluentd git:(master) go run ./entrypoints/main.go --help
{"level":"info","ts":"2019-01-17T16:16:53.463+0800","caller":"go-utils/logger.go:39","message":"Logger construction succeeded","level":"info"}
Usage of /var/folders/v0/02b7gzrx6cq8b66pbk00byf00000gp/T/go-build509430316/b001/exe/main:
      --addr localhost:8080            like localhost:8080 (default "localhost:8080")
      --config string                  config file directory path (default "/etc/go-fluentd/settings")
      --config-server string           config server url
      --config-server-appname string   config server app name
      --config-server-key string       raw content key
      --config-server-label string     config server branch name
      --config-server-profile string   config server profile name
      --debug                          run in debug mode
      --dry                            run in dry mode
      --env sit/perf/uat/prod          environment sit/perf/uat/prod
      --heartbeat int                  heartbeat seconds (default 60)
      --log-level debug/info/error     debug/info/error (default "info")
      --pprof                          run in prof mode
pflag: help requested
exit status 2
```

目前支持的运行参数：

- `--debug`：bool，启用 debug 模式；
- `--dry`：bool，启用 dry 模式（不会发生实际的外部影响）；
- `--pprof`：bool，是否启用性能分析；
- `--config`：string，配置文件所在的文件夹路径；
- `--config-server`：string，config-server 的 URL；
- `--config-server-appname`：string；
- `--config-server-profile`：string；
- `--config-server-label`：string；
- `--config-server-key`：string，config-server 中，yml 内容对应的 key；
- `--addr`：string，监听的 HTTP 地址；
- `--env`：string，当前运行的环境，接收 sit/perf/uat/prod；
- `--log-level`：string，日志级别，支持 debug/info/error；
- `--heartbeat`：int，心跳日志间隔秒数。



### 1.3.2. 配置

命令行参数只接收一些必要的参数，而运行插件所需的参数都来自于配置文件。目前支持两种读取配置的方式：

- 从 yml 文件读取；
- 从 config-server 中加载 yml 文本。

从 yml 文件读取，使用 `--config` 参数传入配置文件所在的文件夹，目前默认配置文件名为 `settings.yml`，默认文件夹地址为 `/etc/go-fluentd/settings`。

从 config-server 读取，需要指定 `--config-server`、`--config-server-appname`、`--config-server-profile`、`--config-server-label`、`--config-server-key`。
也就是会从 `{config-server}/{config-server-appname}/{config-server-profile}/{config-server-label}` 加载 json，
并且从其中的 `{config-server-key}` key 中读取原始的 yml 文件内容进行解析。
