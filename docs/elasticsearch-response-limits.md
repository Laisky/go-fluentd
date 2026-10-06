# Elasticsearch bulk response limits

Set `settings.producer.plugins.<name>.max_response_byte` on an `es`
sender to bound successful-response parsing. Zero selects **4 MiB**; positive
values up to **64 MiB** are supported. Negative or larger values fail startup.
The bound applies to the **decoded** body, including transparently decompressed
gzip and chunked/unknown-length responses. A Content-Length hint cannot bypass
it. The reader consumes at most the configured limit plus one detection byte.

Non-2xx response bodies are not read or included in diagnostics. Malformed,
missing, oversized, and partial-failure bulk responses are never acknowledged as
successful deliveries. A valid explicit `{"errors":false}` is still required;
Elasticsearch's `filter_path=errors` can keep normal bulk responses small.

Protocol/size failures and permanent HTTP errors stop the immediate four-attempt
loop. HTTP 408, 429, 5xx, transport errors, and bulk item failures retain the
existing retry policy. All failed batches keep their original journal identity
and use the failure channel; this does not discard the journaled data. Persistent
failures therefore still require operator remediation rather than an unlimited
busy retry inside one worker. Every acquired response body is closed.

The existing 30-second request timeout is unchanged. The decoded-input bound is
not an exact heap limit: buffer growth and JSON decoding add overhead, multiplied
by concurrent sender workers (`n_fork`). Size worker counts and process memory
accordingly. Response content is never placed in an error message. This policy
does not redact application payloads intentionally enabled in debug logging.
