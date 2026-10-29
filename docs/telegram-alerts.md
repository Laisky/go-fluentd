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
```

When enabled, set `enabled` to true and supply at least one exact Fluent tag or OTLP `service.name` in the allowlists. The variables named by `token_env` and `chat_id_env` must exist at startup. The token must have the Bot API token shape. Only the fixed `https://api.telegram.org` endpoint is used, with no redirects. Choose application sources only: do not select the collector's own logs or a notification service's logs, as those can cause recursive alert storms. The setting does not alter existing application pushers.

Legacy Fluent messages are observed after the journal and before tag/post filters. The plugin reads a string or byte `level`, accepts error, dpanic, panic, fatal, critical/crit, alert and emergency (case insensitive), and uses `msg` or `log` as a message. When `level` is absent, it also inspects a bounded JSON object in `log` for `level` and `msg` or `message`. Missing, malformed and lower levels do not alert. Source is the journal tag. Accepted native OTLP log requests are observed after durable admission, using their original resource, scope and record. Severity numbers 17–24 qualify; if the number is unspecified, recognized severity text qualifies. The source includes service, host and scope. Metrics and traces do not alert. Requests larger than 1 MiB are skipped by the alert observer without changing storage. Selection and notification are independent of downstream storage success.

Message bodies are omitted by default. `include_message: true` sends a bounded text excerpt and should be enabled only after reviewing data classification and the bot/chat audience. Common credential assignments and URLs are redacted, but arbitrary secrets or personal data cannot be reliably detected. Source names also leave the collector. Never rely on this filter as a guarantee of secret removal. Use appropriate upstream sanitization and a restricted destination. Text is sent without Markdown or HTML parsing; oversized input is omitted and output is bounded below Telegram's message limit, including Unicode.

Each process has one worker, a 64-event queue, four pending OTLP requests, and at most 1,024 deduplication entries with a one-minute window. Deduplication is by normalized source, severity and bounded message. It is in-memory and best effort; distinct records can collapse and repeated records can alert again after expiry or restart. Full queues and a full dedup table drop alerts. The worker spaces attempts by at least one second, retries at most twice after the first attempt for transport failures, HTTP 429 and 5xx, and uses bounded exponential backoff. Telegram's `retry_after` is respected; a delay over one hour causes the alert to be dropped rather than retried early. Other responses and malformed success responses fail without retry. A timeout can occur after Telegram accepted a message, so retry can duplicate it. The in-memory queue is not durable and is discarded on shutdown; shutdown cancels in-flight requests. The `telegramAlert` monitor exposes queued, dropped, suppressed, failed and sent counters; it does not include sensitive payloads or response bodies. These counters reset on restart and do not account for all external delivery ambiguity.

OTLP storage replay is not observed again, but a client retry after an uncertain admission can create another admission and an alert, subject to deduplication. Fluent journal replay may produce another alert, subject to the same in-memory window. Collector outages, rejected ingress, filtering before observation, unavailable Telegram and exhausted retries can lose alerts. Monitor collector availability and storage independently.

Migration: first test with a non-production source and a separately provisioned restricted bot/chat, compare coverage against the existing application pushers, monitor drops and failures, and validate privacy and duplicates. Keep existing pushers until the comparison is accepted. Roll back by disabling this setting and restarting; leave application integrations intact. This change does not enable the feature or provision credentials.

References: [Telegram sendMessage](https://core.telegram.org/bots/api#sendmessage), [response parameters](https://core.telegram.org/bots/api#responseparameters).
