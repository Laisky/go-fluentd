# Quick Start

Running a minimal example with app, go-fluentd and fluentd.

App ---> Go-Fluentd ---> Fluentd

- App: generate and emit logs
- Go-Fluentd: collect and parse log
- Fluentd: just a example for backend, it also could be ElasticSearch or something else



## Prepare

Requirements:

- git
- docker
- docker-compose

And this example need bind port 24225 to transfer log stream,
and port 8080 to monitor HTTP API.


## Install & Run

```sh
# clone
$ git clone https://github.com/Laisky/go-fluentd.git

# running
$ cd go-fluentd/example
$ sudo docker-compose up -d --remove-orphans --force-recreate

# check
$ sudo docker-compose ps

         Name                        Command               State                          Ports
-----------------------------------------------------------------------------------------------------------------------
example_fluentd_1         /bin/entrypoint.sh /bin/sh ...   Up      24224/tcp, 5140/tcp
example_go-fluentd_1      ./go-fluentd --config=/etc ...   Up      127.0.0.1:24225->24225/tcp, 127.0.0.1:8080->8080/tcp
example_log-generator_1   python /app.py                   Up
```

The origin logs emitted by app are look like:

```
2019-02-28 08:41:21.123 | app | INFO | thread | class | 64: xxxx
```

You can check the logs that parserd by go-fluentd:

```sh
$ sudo docker logs example_fluentd_1

2019-02-28 08:41:21.534852412 +0000 test.sit: {"tag":"test.sit","app":"app","thread":"thread","class":"class","message":"0.8336017742577866\n0.059360002847527626\n0.9471091772460405","msgid":1377,"container_name":"/example_log-generator_1","source":"stdout","level":"INFO","line":"64","datasource":"test","@timestamp":"2019-02-21T07:41:17.871000Z","container_id":"24d6069f241ad94719ac1eee15dce43e29a3a32af67c478ded9c474066389260"}
2019-02-28 08:41:21.539087614 +0000 test.sit: {"container_name":"/example_log-generator_1","msgid":1026,"line":"64","message":"0.7159115118036709","datasource":"test","level":"INFO","thread":"thread","class":"class","@timestamp":"2019-02-03T17:41:13.813000Z","container_id":"24d6069f241ad94719ac1eee15dce43e29a3a32af67c478ded9c474066389260","source":"stdout","tag":"test.sit","app":"app"}
```

With format:

```js
{
    "tag": "test.sit",
    "app": "app",
    "thread": "thread",
    "class": "class",
    "message": "0.8336017742577866\n0.059360002847527626\n0.9471091772460405",
    "msgid": 1377,
    "container_name": "/example_log-generator_1",
    "source": "stdout",
    "level": "INFO",
    "line": "64",
    "datasource": "test",
    "@timestamp": "2019-02-21T07:41:17.871000Z",
    "container_id": "24d6069f241ad94719ac1eee15dce43e29a3a32af67c478ded9c474066389260"
}
{
    "container_name": "/example_log-generator_1",
    "msgid": 1026,
    "line": "64",
    "message": "0.7159115118036709",
    "datasource": "test",
    "level": "INFO",
    "thread": "thread",
    "class": "class",
    "@timestamp": "2019-02-03T17:41:13.813000Z",
    "container_id": "24d6069f241ad94719ac1eee15dce43e29a3a32af67c478ded9c474066389260",
    "source": "stdout",
    "tag": "test.sit",
    "app": "app"
}
```


## Monitor

You can load monitor metrics by <http://localhost:8080/monitor>


<details><summary>metrics return by monitor HTTP API: </summary>
<p>

```js
// 20190228163221
// http://localhost:8080/monitor

{
    "acceptorPipeline": {
        "msgPerSec": 81.1
    },
    "controllor": {
        "goroutine": 37,
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
        "msgPerSec": 81.1,
        "test.sit.ChanCap": 10000,
        "test.sit.ChanLen": 0,
        "test.sit.MsgPerSec": 81.1
    },
    "producer": {
        "discardChanCap": 50000,
        "discardChanLen": 0,
        "msgPerSec": 36.7,
        "test.sit.fluentd.ChanCap": 50000,
        "test.sit.fluentd.ChanLen": 0,
        "waitToDiscardMsgNum": 0
    },
    "tagpipeline": {
        "test.sit.concator_tagfilter.ChanCap": 10000,
        "test.sit.concator_tagfilter.ChanLen": 0,
        "test.sit.spring.ChanCap": 10000,
        "test.sit.spring.ChanLen": 0
    },
    "ts": "2019-02-28T08:32:21.560776792Z"
}
```
</p>
</details>


## Profile


This HTTP API also support pprof endpoints:

- <http://localhost:8080/pprof/profile>
- <http://localhost:8080/pprof/cmdline>
- <http://localhost:8080/pprof/symbol>
- <http://localhost:8080/pprof/goroutine>
- <http://localhost:8080/pprof/heap>
- <http://localhost:8080/pprof/heap?debug=1>
- <http://localhost:8080/pprof/threadcreate>
- <http://localhost:8080/pprof/debug/block>
- <http://localhost:8080/pprof/>

