# Telegram log alerts (opt in)

This optional observer sends best-effort notifications for selected ingested application logs. Existing configurations leave it disabled. It does not replace storage or collector health monitoring. It uses the Telegram Bot API's `sendMessage` with plain text, not the application GraphQL pusher. No credentials belong in this file or a checked-in configuration.

Example (the environment variables must be supplied separately by the operator):

```yaml
settings:
  telegram_alert:
    enabled: false
    token_env: GOFLUENTD_TELEGRAM_BOT_TOKEN
    chat_id_env: GOFLUENTD_TELEGRAM_CHAT_ID
    tags: [example.application]
    services: [example-service]
    include_message: false
    queue_size: 64
    otlp_queue_size: 4
    dedup_entries: 1024
    dedup_window: 1m
    send_interval: 1s
    max_attempts: 3
```

When enabled, set `enabled` to true and supply at least one exact Fluent tag or OTLP `service.name` in the allowlists. The variables named by `token_env` and `chat_id_env` must exist at startup. The token must have the Bot API token shape. Only the fixed `https://api.telegram.org` endpoint is used, with no redirects. Choose application sources only: do not select the collector's own logs or a notification service's logs, as those can cause recursive alert storms. The setting does not alter existing application pushers.

Legacy Fluent messages are observed as they leave the journal, before dispatcher tag admission and tag/post filters. This includes replay and, where allowed, records that bypass durable journal storage. An alert therefore means an event was observed, not that downstream processing or delivery succeeded; even a subsequently rejected tag can alert. The plugin reads a string or byte `level`, accepts error, dpanic, panic, fatal, critical/crit, alert and emergency (case insensitive), and uses `msg` or `log` as a message. When `level` is absent, it also inspects a bounded JSON object in `log` for `level` and `msg` or `message`. Missing, malformed and lower levels do not alert. Source is the journal tag. Accepted native OTLP log requests are observed after durable admission, using their original resource, scope and record. Severity numbers 17–24 qualify; if the number is unspecified, recognized severity text qualifies. The source includes service, host and scope, and the source timestamp when present; otherwise a collector observation time is shown. Metadata controls and line separators are replaced with spaces. Arbitrary OTLP severity text is not rendered. Metrics and traces do not alert. Requests larger than 1 MiB are skipped by the alert observer without changing storage. Selection and notification are independent of downstream storage success.

Message bodies are omitted by default. `include_message: true` sends a bounded text excerpt and should be enabled only after reviewing data classification and the bot/chat audience. Common credential assignments and URLs are redacted, but arbitrary secrets or personal data cannot be reliably detected. Source names also leave the collector. Never rely on this filter as a guarantee of secret removal. Use appropriate upstream sanitization and a restricted destination. Text is sent without Markdown or HTML parsing; oversized input is omitted and output is bounded below Telegram's message limit, including Unicode.

One bounded parser and one independent delivery worker prevent Telegram delays from stalling OTLP decoding. The defaults shown above preserve the original PR's limits. Operators may set `queue_size` to 1–4096 events, `otlp_queue_size` to 1–32 requests (each at most 1 MiB), `dedup_entries` to 1–16384, `dedup_window` to 1s–1h, `send_interval` to 100ms–1m, and `max_attempts` to 1–5. Invalid enabled policy settings fail startup. These bounded settings affect per-process memory, grouping and delivery; they do not change the journal.

Deduplication groups by the displayed source, normalized severity and original numeric OTLP severity, including the bounded message only when `include_message` is true. Timestamp is context and does not split a group. A suppressed event is counted, not included as an occurrence count in the Telegram message. One arbitrary entry is evicted when the table is full, so new sources are not permanently blocked by one noisy source; there is no strict per-source fairness or oldest-first guarantee. Grouping is in-memory and best effort: distinct records can collapse, and repeated records can alert again after expiry, eviction or restart. Full queues drop observations. A single worker spaces attempts by the configured interval and retries transport failures, HTTP 429 and 5xx with bounded exponential backoff up to the attempt limit. Telegram's `retry_after` is respected; a delay over one hour causes the alert to be dropped rather than retried early. Other responses and malformed success responses fail without retry. A timeout can occur after Telegram accepted a message, so retry can duplicate it.

The queue is not durable. Shutdown cancels in-flight delivery and accounts for queued or in-flight events and pending requests abandoned during shutdown. `/monitor` retains the legacy mixed-unit `dropped` count for compatibility and adds separate request and event counters. Fixed, unlabeled `gofluentd_logging_alert_*` Prometheus metrics expose dropped request and event totals, request loss reasons, deduplication and eviction, retries and failures, sent notifications, queue depths, worker status and shutdown loss. Request counts cannot be interpreted as event counts. Counters reset on restart, shutdown counters may not be scraped before process exit, and neither reports external delivery ambiguity. No payload, destination, source or credential is exported as a metric label.

OTLP storage replay is not observed again, but a client retry after an uncertain admission can create another admission and an alert, subject to deduplication. Fluent journal replay may produce another alert, subject to the same in-memory window. Collector outages, rejected ingress, filtering before observation, unavailable Telegram and exhausted retries can lose alerts. Monitor collector availability and storage independently.

Migration: first test with a non-production source and a separately provisioned restricted bot/chat, compare coverage against the existing application pushers, monitor drops and failures, and validate privacy and duplicates. Keep existing pushers until the comparison is accepted. Roll back by disabling this setting and restarting; leave application integrations intact. This change does not enable the feature or provision credentials.

References: [Telegram sendMessage](https://core.telegram.org/bots/api#sendmessage), [response parameters](https://core.telegram.org/bots/api#responseparameters).
