# External delivery contract

The oracle is defined independently of application queues, message pools, sender
success channels, journal counters, and coverage. It is an external producer
ledger and one independently fsynced receive ledger for every required sink.

For each validated message accepted by the reliable HTTP endpoint:

* All required sinks must eventually receive its exact producer-generated key
  and payload, after failures stop and the process is restarted as needed.
* No unrelated event may appear. A duplicate is allowed only with identical
  content. Repeated delivery is not exactly-once delivery.
* A successful acceptance response must not precede durable local ownership.
  Storage refusal or pre-persistence policy rejection must not return success.
* A sink rejecting or losing an acknowledgement must not permit deletion of the
  only recoverable copy. One successful sink cannot speak for another.
* Restart must not reuse a delivery identity that can still refer to retained
  records. Retained payloads must not alias newly accepted payloads.
* Once all sinks have acknowledged and confirmation has settled, a clean restart
  must not repeatedly redeliver confirmed records.

Tests launch the normal application executable with generated configuration,
real TCP/HTTP sockets and temporary disk. Crash means SIGKILL followed by a fresh
process using the same directory: no graceful-close hook, no manual journal
repair, no direct call to an internal replay function. Failure scheduling uses
observable network handshakes; bounded waits diagnose liveness, not success.

Invalid signatures, explicit filter rejections, best-effort inputs and unfinished
requests are not silently counted as reliably accepted. Tests for those paths
assert their separate observable contract. These tests do not simulate loss of
OS page cache or physical storage failure, and protocol peers are not real
Elasticsearch/Kafka clusters. TCP write completion is not a durable receiver ACK.
