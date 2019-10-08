<https://blog.laisky.com/p/go-fluentd/>

# 使用 Golang 写一个取代 fluentd 的日志处理器

> Changelog:
>
> * updated at 2019/9/30：一些优化的说明

## 起因

我曾经写过一篇介绍 fluentd 的文章：<https://blog.laisky.com/p/fluentd/>，
在此文中，我介绍了使用 fluentd 作为日志处理工具的用法，在工作中，我也使用了多年的 fluentd。而且 fluentd 还加入了 CNCF，发展潜力可观。

不过在目前的公司中，因为较为特殊的需求，使用 fluentd 遇到了一些问题。
首先是插件质量良莠不齐，而因为没有熟悉 ruby 的人，所以调试起来相当头疼。
其次是 ruby 存在 GIL，性能被限制在单核上，而 process 扩展的可用场景有限。
最后就是实际需求过于多样，fluentd 的很多插件没法满足需求，
或是需要大量使用 rewrite 控制解析流，配置文件相当晦涩难懂。
综合这些原因，正好最近没什么事，以及也想看看 golang 的能力，
所以干脆决定用 golang 自己写一个。

如果按照 collector, aggregator, concator, parser, forwarder 来理解日志流的话，
那我就是实现了除 collector 外的所有功能，也就是说，实现了支持 fluentd 协议的完整服务端，
客户端可以完全无感的继续按照过去使用 fluentd 的形式向远端推送日志。

主要的开发工作大概花了 3 个月，然后在一个集群环境上完全取代了 fluentd，
这个集群的日志量大约为 1.5 万条/秒，使用 fluentd 时开了 1 个 concator，4 个 parser + forwarder，
在一台八核虚机上吃掉了绝大部分 CPU，golang 新版上线后 CPU 变化不大，
然后又花了大概一个月进行性能优化（借助 pprof），这期间集群的流量也上升到了 2 万/秒（50 mbps），而且增加了日志内 json 解析的需求，但是 CPU 使用率下降到了 400% 以下，成效卓著。（在四核虚机上简单测试，应该能达到 2-30万/秒 的吞吐，不过性能和日志结构的相关性很大，仅作参考）

此文大致介绍一下 go-fluentd 的设计和开发历程，留作参考。很多设计不一定最优甚至有些拙劣，不过在较短时间内，完全支撑了线上业务，并且留出了相当大的性能冗余，应该还可以算是有一定价值。

项目代码：<https://github.com/Laisky/go-fluentd>


---

## 功能组件

首先，最早的需求场景就是，数百个 IP 以 TCP 的形式推送日志流，每一条日志都有相对固定的格式，但是一条日志可能会被拆分为数个不同的数据，所以需要监听连接 -> 接收日志流 -> 解码日志 -> 然后识别拼接同一条日志 -> 将日志字符串解析为结构化的数据 -> 发送到后端（ElasticSearch）。

然后在实际开发中，发现还需要实现日志 At Least Once 的保证，所以又增加了 journal 的设计，每当拿到一条日志数据后，进在文件中进行持久化，每当这条日志成功发送到后端后，再在文件中将该日志标记为已发送。

所以我在设计上，采用了插件化的设计，将日志从接收到转发的全部流程拆分为一个个不同的步骤，
每个步骤都可以按需的插入功能组件（只要插件实现了所要求的接口方法），然后每一个步骤在接收到日志数据后，按顺序调用一遍插件，然后再放入后续流程的 channel 即可，目前已经拆分的步骤有：

- 日志接收：acceptor + recvs
- 日志预处理：acceptorPipeline
- redo log：journal
- 日志解析：dispatcher + tagFilters
- 日志后处理：postPipeline
- 日志转发：producer + senders


除了步骤外，还有两个工具类：

- 监控：monitor
- 控制：controllor

![architect](https://s3.laisky.com/uploads/2019/01/go-fluentd-architecture.jpg)

需要强调的是，在整个流程中流转的日志数据体，都封装为 `*libs.Fluentd` 数据结构（下文中以 msg 代称）：

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


不过为了方便，在 Message 中，也放了几个保留字段用来保存元数据：

- `tag`：用来保存该消息真实的 tag（因为 msg.Tag 有时候会为了流转而被不断的改动）；
- `msgid`：等于 msg.Id，可用于对消息进行去重。


### Acceptor & Recvs

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
- `asyncOutChan`：不会被阻塞，journal 阻塞后会跳过 journal，如果后续仍然堵塞就会丢弃消息，用于确保服务不会因为日志消费不及时受到影响；


#### fluentd-recv 的一些问题

fluentd 采取了 messagepack 的形式来编解码数据，最初一版里我采用了 <https://github.com/ugorji/go>，但是在开发过程中感觉这个库蛮难用的，后来在性能调优的时候，通过性能测试发现这个库的性能也很差，所以就换成了 <https://github.com/tinylib/msgp>。这个库不但使用简单，而且性能极强，目前在我的 pprof 火焰图中基本已经看不到 messagepack 编解码的消耗。

然后发现了 docker 的一个坑（准确的说是一系列坑），对于 17、18 的几个版本，
使用 `--log-driver=fluentd` 时，如果远端日志处理不及时导致缓存堆积，
最终导致 tcp winsize 置为 0 时，docker 会阻塞内部进程的输出操作。
目前我采取了两种方法，一是在客户端设置了

```
--log-opt fluentd-async-connect=true --log-opt mode=non-blocking
```

在 v17.05 上测试没问题，不过在 v18 上发现依然出现了阻塞。
所以在设计 go-fluentd 的 fluentd-recv 时，加了一个策略，尽可能快的读取数据，
如果后续处理不及时时，宁愿丢弃消息也不能阻塞 tcp。实现的方法就是上述的 `syncOutChan/asyncOutChan`。


2019/9/30 补充：在 v1.11.0 后，把拼接的逻辑放进了 fluentd-recv 里，
原因是 acceptorPipeline 是并行的，这会导致日志片段（fragments，也就是被拆碎的日志）出现乱序，
为了确保能够按照正确的顺序进行拼接，所以把拼接的逻辑放进了 fluentd-recv
（反正似乎也只有 docker fluentd-driver 需要拼接。）


### AcceptorPipeline

recv 会输出大量的 msg 数据，不过并不是所有的数据都是有效的（甚至有接近一半都是无效的），
如果把这些数据全部都持久化硬盘的话有些不值，所以在 journal 前加了一步简单的过滤，
过滤掉一些明显不符合要求的数据。

除此之外，有一些 msg 需要根据内容改变自己的 tag，这一步也放在这里做了。

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


各个 filters 返回 nil 的话，就会跳过后续的 filters。
但是由于 filter 可以将消息重新放入 inChan，
所以即使返回 nil，Pipeline 也无法判定消息是否还要使用，
如果丢弃消息的话，各个 filter 需要自行调用 `DiscardMsg` 进行回收。

需要注意的是，acceptorFilters 早于 journal，消息尚未被持久化到磁盘，
所以为了提高可靠性，不要在这里实现太复杂的逻辑，应该让 msg 尽可能快的通过。


### Journal

Journal 扮演 redo log 的角色，将 AcceptorPipeline 后的所有消息都写入 data 文件，提供持久化。
任何被后续流程处理的消息，也需要将其 id 提交给 journal 并写入到 ids 文件中。

journal 会定期的重新读取历史的 data 和 ids 文件，首先加载所有的 ids，然后再逐一的读取 data 中的 msg，
如果 msg 的 id 已经存在于 ids 中，说明该消息已被处理，则跳过。
如果 msg 的 id 未存在于 ids 中，说明该消息还没有被后续流程处理，则将该 msg 传递给 journal dump，
也就是将其写入新的 data 文件，并进入后续流转。这样可以保证任何达到了 journal 的消息，至少会被消费一次，实现 At Least Once 语义。

在程序中止时，只有 Acceptor 和 AccptorPipeline 内的消息和 Journal 内正在等待 dump 的消息会丢失，
所以 Acceptor 和 AccptorPipeline 的处理要尽可能的快，而 Journal dump 的目标磁盘的 IO 也要尽可能的高。
不过由于 Journal 使用的磁盘空间极小（视配置而定，一半小于 1 GB），而且全部采用顺序读写，所以应该能很好的利用页缓存，不会对磁盘带来太大压力。

data 文件采用 messagePack 编码，因为这样最简单，该编码带长度信息，而且不需要预定义 schema，压缩大小和 protobuf 也差不太多。

ids 文件采用简单的 binary Big Endian 存储定长的 int64。


#### 问题

这个组件设计的比较草率，因为一开始没意识到还需要 journal，后面加的时候也比较仓促。
目前用下来最主要的几个问题有：

- 消息重复
- 文件损坏的处理
- 性能

因为消息被持久化到消息被消费间有一个时间差，这个时间差内如果 journal 文件发生了 rotate，
那么这一批次的数据都会被重新放入流水线进行处理。目前采取的办法也很粗糙，
每次 rotate 的时候同步创建和切换新的 data 和 id 文件，而且加快 rotate 的频率，
尽可能的减少每次 rotate 的消息数量，目前线上来看消息的重复率已经相当的低。
不过还有很大的优化空间。

当初设计的时候没有考虑文件损坏的问题，其实如果在写入的时候断电或重启，
由于缓存的存在，文件还是比较容易损坏。后来加入的临时解决办法是，
一旦从文件中读取数据出错，就跳过该文件。至少保证了服务的稳健运行，
而且由于机器重启或断电的概率极低，目前这还不构成问题。

就目前的观察来说，性能问题不大，benchmark 的时候也可以轻松的跑满磁盘 IO
（当然一个原因是因为我们线上用的是最便宜的网络磁盘，吞吐仅有 128 mbps）。
而且通过 iostat 等工具观测，由于实际上 journal 产生的文件很小（默认每个 200 MB，一般总数不会超过 2 GB），
所有的文件都可以被页缓存，对磁盘没有任何读操作。
不过看上去写磁盘时并没有触发写合并，因为不是瓶颈所以没有再花时间去多看。

另一个小问题是，从 pprof 上来看，`File.Stat` 的开销还不小，看来这个操作也挺费时的。


#### 2019/9/30 优化

后来对 journal 尝试做了很多优化。

首先是尝试了启用 gzip，但是发现启用 gzip 后，对于 50KB 的数据，
即使是采用 best speed，也要慢接近 80 倍。
按照 291897ns 处理 50KB 的速度，每秒钟可以处理的数据为 167 MB。
考虑到写文件的速度为 412151ns 的话，每秒处理速度为 118MB。

```
BenchmarkGZCompressor/normal_write_50KB-4    	  531210	      2313 ns/op	       0 B/op	       0 allocs/op
BenchmarkGZCompressor/gz_write_50kB_default-4        	     652	   1593705 ns/op	       0 B/op	       0 allocs/op
BenchmarkGZCompressor/gz_write_50kB_best_compression-4         	     783	   1491124 ns/op	       0 B/op	       0 allocs/op
BenchmarkGZCompressor/gz_write_50kB_best_speed-4               	    4370	    291897 ns/op	       0 B/op	       0 allocs/op
BenchmarkGZCompressor/gz_write_50kB_HuffmanOnly-4              	    4652	    250891 ns/op	       0 B/op	       0 allocs/op
BenchmarkGZCompressor/gz_write_50KB_to_file-4                  	     286	   5067483 ns/op	       0 B/op	       0 allocs/op
BenchmarkGZCompressor/gz_write_50KB_to_file_best_speed-4       	    3494	    412151 ns/op	  148759 B/op	       0 allocs/op
BenchmarkGZCompressor/gz_write_50KB_to_file_BestCompression-4  	     690	   1596123 ns/op	       0 B/op	       0 allocs/op
```

不过因为启用 gzip 后会显著增加 CPU 开销，所以将默认配置项设置为了 false。

另一个优化是，以前是把所有的文件都写入同一个 journal file，后来做了优化，
每一个 tag 都实例化一个 journal，写入到不同的文件中，极大的分散了 IO。
这样即使某些项出了问题，也不会干扰到其他 tag。

```sh
[root@qing-dataplatform-srv4 laisky]# du -sh /data/go-concator/*
200M    /data/log/fluentd/go-concator/ai.prod
200M    /data/log/fluentd/go-concator/app.spring.prod
201M    /data/log/fluentd/go-concator/base.prod
200M    /data/log/fluentd/go-concator/bigdata-wuling.prod
200M    /data/log/fluentd/go-concator/bot.prod
201M    /data/log/fluentd/go-concator/connector.prod
200M    /data/log/fluentd/go-concator/cp.prod
200M    /data/log/fluentd/go-concator/emqtt.prod
200M    /data/log/fluentd/go-concator/gateway.prod
201M    /data/log/fluentd/go-concator/geely.prod
200M    /data/log/fluentd/go-concator/qingai.prod
200M    /data/log/fluentd/go-concator/spark.prod
201M    /data/log/fluentd/go-concator/tsp.prod
200M    /data/log/fluentd/go-concator/usertracking.prod
200M    /data/log/fluentd/go-concator/wechat.prod
```

然后为了减少日志的重复率，默认至少保存一个 journal buf file，
也就是除了当前正在写入的那个文件外，额外会保留最近一份文件不作处理。
因为按照以前的逻辑，有可能一个 log 刚被写入文件就遇到了 rotate，然后立刻就被加载处理了，
从而导致重复。

同样是为了减少日志重复率，在内存中更长时间的保存 committed ids。
可以通过 `settings.journal.committed_id_sec` 设置。

最后一个优化就是启用了磁盘预分配，所以看到的文件夹大小都是 200MB。


### Dispatcher & tagFilters

其实最初导致 Fluentd 不能水平扩展的根本原因就在于我们需要让日志以 tag & container_id 来分流，
而目前使用的 lb（如 haproxy） 只支持 roundrobin 或 ip source，都不符合需求。
所以首先使用 dispatcher 按照 tag 对 msg 进行分流（按照 tag 启动不同的 goroutine，并放入起 channel 中），
每一个 channel 后都接上支持该 tag 的 tagfilters。最后再进行合流，以一个 channel 进行统一输出。

简单的说，dispatcher 就是一个负载均衡，利用后续的多个 goroutine 来让解析压力分散到每一个 CPU 上。

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


#### 问题

其实，一切问题的起源都是因为 docker 的 `--log-driver=fluentd` 会对消息按照 `\n` 进行拆行，
为了拼接就必须负载均衡器必须要保证前后端连接的一致性，而保证这个一致性就导致 fluentd 性能不足，
因为云环境上 IP 高度集中，采用 source ip 策略做 LB 后的流量高度倾斜，几乎全部压在几个节点上。
而为了更好的 LB，就需要对 tag 做 LB，这也是 dispatcher 的设计目的。

但其实…如果当初不用 docker 的 log-driver，而是用日志组件直接输出日志，那么就根本不存在拆行问题，
如果不需要拼接的话，后端就不会有状态，而无状态的话就可以用 round robin 非常轻松的做 LB 和水平扩展…

不过反正搞都搞了，而且在做解析需求的时候确实顺手了很多…

目前，正则解析占了大约 25% 的 CPU。不过这是因为我们的数据都偏大，而且要做多次解析。


### PostPipeline

负责对已解析的日志进行后处理，该步骤结束后就直接进入转发步骤。
内部设计理念和 acceptorPipeline 完全一致，postFilters 的接口要求为：


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


### Producer & Senders

Producer 负责转发消息到其他服务（如 ElasticSearch），
支持多 senders，按照 msg.Tag 传递给 sender 进行发送，
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
  - 如果有任意 msg id 是通过 discardWithoutCommitChan 送回的，则仅回收消息，
    但是不向 journal 提交（这会导致该条消息在稍后会被重新处理，适用于消息发送失败，决定稍后重试的场景）。


目前支持的 sender 有：

- elasticsearch
- fluentd
- http
- kafka


### Controllor

这不是流程，而是代码的一个设计模式，在 controllor 内，对所有的流程组件进行初始化，
并且进行拼接，最终实现完成的流程，可以理解为类似于 IoC 容器。

具体的代码就是 `controllor.go` 内，根据配置文件，加载不同的功能组件，
通过 channel 串联或并联为流水线。


### Monitor

一个基于 iris 提供 HTTP 监控接口的组件。通过 `HTTP GET /monitor` 返回监控结果。

需要手动采集监控数据，在代码的任何地方，
都可以通过调用 monitor.AddMetric 为最终的监控结果（以 json 呈现）中添加一个字段：

```go
func AddMetric(name string, metric func() map[string]interface{}) {
    metricGetter[name] = metric
}
```

其实就是定义一个函数，输出监控的字段和数值，每次监控接口被调用时，都会调用这些函数。
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



---

## 运行

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



### 配置

命令行参数只接收一些必要的参数，而运行插件所需的参数都来自于配置文件。目前支持两种读取配置的方式：

- 从 yml 文件读取；
- 从 config-server 中加载 yml 文本。

从 yml 文件读取，使用 `--config` 参数传入配置文件所在的文件夹，目前默认配置文件名为 `settings.yml`，默认文件夹地址为 `/etc/go-fluentd/settings`。

从 config-server 读取，需要指定 `--config-server`、`--config-server-appname`、`--config-server-profile`、`--config-server-label`、`--config-server-key`。
也就是会从 `{config-server}/{config-server-appname}/{config-server-profile}/{config-server-label}` 加载 json，
并且从其中的 `{config-server-key}` key 中读取原始的 yml 文件内容进行解析。

其中一些重要的经常变动的组件，如 senders、recvs、tagFilters，
已经可以根据配置文件来加载所需的插件，而需要改动代码。不过并不支持动态加载。


---

## 性能调优

golang 的性能调优真是器大活好！

我使用了 iris 提供的 HTTP 在线导出 pprof 数据的功能。
用法非常简单，只需要注册一个 endpoint：

```go
// supported action:
// cmdline, profile, symbol, goroutine, heap, threadcreate, debug/block
Server.Any("/admin/pprof/{action:path}", pprof.New())
```

然后就可以调用：

- `/admin/pprof/cmdline`：启动时的命令行参数
- `/admin/pprof/profile`：下载 CPU dump
- `/admin/pprof/symbol`
- `/admin/pprof/goroutine`：goroutine 信息
- `/admin/pprof/heap`：下载 heap dump
- `/admin/pprof/heap?debug=1`：一些运行时信息
- `/admin/pprof/threadcreate`
- `/admin/pprof/debug/block`
- `/admin/pprof/`


### CPU Profile

针对 CPU 做优化时最常用的就是 profile，访问 `/admin/pprof/profile` 后，
会自动启动采样，等待 30 秒后，会下载一个 `profile` 文件，然后就可以启动 go tool 的交互式工具：

```sh
go tool pprof --http=:8000 profile
```

默认显示的应该是 graph：

![profile-graph](https://s3.laisky.com/uploads/2019/02/profile-graph.jpg)

线条代表调用关系，方框代表函数，框越大代表占用的时间越多。

graph 最大的优点在于让你可以一眼就看出什么函数占用了最长的执行时间。
也就是对最热点的调用一目了然，让你在下一步的优化中可以有的放矢。

比如你有一个工具函数被调用了绝大多数次，那么在 graph 中将会一目了然，
而在 flamegraph 却可能因为调用链分散而体现不出来，
这就是 graph 相对于 flamegragh 的优点。


通过点击左上角的 view，还可以切换到 flamegraph：

![profile-flamegraph](https://s3.laisky.com/uploads/2019/02/profile-flamegraph.jpg)

火焰图可以更清晰的展示逻辑的调用链，从 root 开始，一层层的往下调用，
每一次调用的耗时占比也是一目了然。让你知道你的代码逻辑中，哪一个步骤耗时最多。

从性能分析中可以看出，go-fluentd 在当前的负载下，耗时最多的有：

- `regexp.FindSubmatch`：大量的正则确实很费时；
- `compress.gzip`：压缩也很耗 CPU；
- `msgp.Decode`：recv 解码数据流，经过优化后已经少了很多；
- `runtime.park_m`：goroutine 在等待资源，说明此时计算资源还相当的空闲；
- `gc.bgMarkWorkers`：GC。


可以看出，通过优化，messagepack 和 json 的编解码开销已经几乎可以忽略不计。
顺带一提，当你发现有大量的 `runtime.park_m` 时，说明此时机器资源非常空闲，
所以此时火焰图上呈现的数据不一定是瓶颈所在，比如你甚至可能看到大量的 `sleep` 调用占了很多的 CPU，
但其实当负载上升后，这些调用的占比就会小到忽略不计。

所以，建议在压测的时候做 profile，这时候最能体现出整个流程的瓶颈。
而且，有很多问题，不在高负载下根本不会出现。比如下面提到的 zap 问题。


### 日志 zap 的问题

在压测的时候，通过 pprof 发现出现了大量的 `time.Now` 调用，
严重时甚至能有占用超过 20% 的 CPU。

![profile-zap](https://s3.laisky.com/uploads/2019/02/profile-zap.jpg)

通过 strace 后发现确实有非常大量的 `clock_gettime` 调用。
通过排查后发现，只要我注释掉 DEBUG 日志，这一问题就会消失（即使我的 log level 设置为 INFO）。

所以我怀疑是我用的日志组件 zap 出了什么问题。查看 zap 源代码后找到了问题所在：

```go
func (log *Logger) check(lvl zapcore.Level, msg string) *zapcore.CheckedEntry {
	// check must always be called directly by a method in the Logger interface
	// (e.g., Check, Info, Fatal).
	const callerSkipOffset = 2

	// Create basic checked entry thru the core; this will be non-nil if the
	// log message will actually be written somewhere.
	ent := zapcore.Entry{
		LoggerName: log.name,
		Time:       time.Now(),
		Level:      lvl,
		Message:    msg,
	}
	ce := log.core.Check(ent, nil)
	willWrite := ce != nil
```

在通过 `log.core.Check` 对 log level 进行检查前，先生成了 log entry，
而且其中调用了 `time.Now()`。

问题在于，`time.Now()` 会导致一次 `clock_gettime` 的系统调用，
而对于 IO 密集型应用，每一次系统都应该精打细用，在高负载下，
这一操作导致了大量的调用堆积，也导致 cpu load 畸高。

解决思路有两个：一是对时间进行缓存，因为其实你并不需要每一次都请求精确的时间。
另一个方法就更简单粗暴，只对要输出的日志生成时间，没达到输入级别的日志则直接丢弃。

所以我就简单粗暴的加了一行 if，性能提升显著：

![new-zap](https://s3.laisky.com/uploads/2019/02/zap_benchmark.jpeg)

给 zap 提了一个 pr，但是一直没人理，所以现阶段，如果使用 zap 的话，
建议换成我的：`import "github.com/Laisky/zap"`。
