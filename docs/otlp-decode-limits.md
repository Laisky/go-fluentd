# OTLP pre-materialization limits

The receiver retains its independent encoded/decompressed byte limits, bearer
validation, active-request semaphore and network read deadlines. After reading
those bounded bytes, it now performs a non-materializing structural preflight
**before** invoking the selected OpenTelemetry pdata decoder. The original
payload remains unchanged; a successful structural preflight does not replace
pdata's schema validation or the final telemetry-item count.

## Admission policy

`max_items` counts log records, spans, or metric data points. All five metric
kinds are covered, including histograms and summaries. Preflight charges every
encoded known item occurrence, including duplicate oneof values or deprecated
containers that pdata may later overwrite or discard: their allocation work
still exists. The final request's `Items()` continues to use pdata's count.

Two independent fixed limits also apply: **65,536 structural units** and a
**64-level nesting limit** per request. JSON charges values, object keys, arrays
and objects. Protobuf charges messages, fields and each packed numeric element.
Thus empty resource/scope containers, attributes, events, links, histogram
buckets and zero-known-item requests cannot bypass structural admission. The
units and depth are encoding-specific, not a promise that equivalent JSON and
protobuf use identical budgets. Split unusually complex batches rather than
raising only `max_items` or byte limits.

JSON syntax and UTF-8 validation scan the bounded input without constructing an
object graph. Known field spellings (including existing pdata snake_case aliases
and escaped JSON keys) receive the same checks. Unknown JSON fields remain
accepted within the structural limits. Unknown protobuf length fields remain
opaque; their bytes are not guessed to be nested messages. Unknown groups are
checked with bounded nesting and matching end markers. No original bytes are
removed or rewritten.

Every budget failure maps to the existing HTTP **413** path and happens before
the durable-admission callback. Saturated concurrency remains HTTP **503**.
Neither response acknowledges journal acceptance. Each rejected request releases
its active slot; no budget or item counter is retained between requests.

## Memory and operational boundary

These are finite input/object-work bounds, **not an exact heap or RSS quota**.
Body buffers, gzip buffers, string/slice growth, decoded objects, stack space,
GC retention and later pipeline state also consume memory. The configured
service permits 16 active decode/admission requests by default, so up to 16
bounded object graphs and body buffers can coexist. With default limits this
means at most 16 preflights/decodes, each bounded by 4 MiB input/expanded bytes,
10,000 item occurrences, 65,536 structural units and depth 64; it does not mean
that 16 times 4 MiB is the process memory requirement. Multiple receiver
instances or separate Fluent ingress have their own budgets.

Size `max_concurrent`, byte limits and process/container memory limits together.
A memory-constrained host should use smaller limits and representative capacity
measurements, not assume wire-byte limits are a decoder allocation quota. This
change does not qualify b1/home production capacity or delete retained work.

## Regression and schema maintenance

`TestRegressionOTLPRejectsBeforeMaterialization` sends 65,536 synthetic empty
items through the real `ReadRequest` path with an eight-item limit. The bounded
fixtures are 128–192 KiB, not an OOM experiment. The original merged baseline
allocates about 9.4–17.9 MB before rejection; the fixed path allocates about
0.31–0.46 MB including input buffering on Go 1.27.1 linux/amd64. The regression
allows 3 MiB rather than relying on exact allocator values. These are total
allocated-byte observations, not peak heap/RSS or production throughput.

Schema metadata is generated from **all reachable generated pdata decoders**,
including known embedded messages and packed primitives. The generator records
a fingerprint of their source, and CI rejects stale metadata when pdata changes:

```sh
python3 .scripts/generate_otlp_preflight_schema.py --check
# For a local offline vendored workspace:
python3 .scripts/generate_otlp_preflight_schema.py --check \
  --pdata-dir vendor/go.opentelemetry.io/collector/pdata
```

Regeneration is not automatic approval of a dependency upgrade. Review changed
field/count semantics and retain boundary, gzip, unknown-field, nested-structure,
allocation, HTTP non-admission/concurrency, fuzz and race regressions.
