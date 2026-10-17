# Credential-safe startup diagnostics

Configuration structs and arbitrary plugin maps are never printed. Startup logs
only fixed messages, the configuration file path, the selected log level, and
the number of receiver plugins. This applies to ordinary and debug startup.
Configuration decoding errors do not include offending values or arbitrary field
names, including malformed OTLP durations and nested destination settings. OTLP
decoding failures retain their typed cause for programmatic inspection through
`errors.Is` / `errors.As`; never log the unwrapped cause.

HTTP and Elasticsearch sender startup and request-error diagnostics use one
allowlisted URL representation: HTTP(S) scheme, host, and escaped path. Entire
userinfo, all query parameters (including unknown names), and fragments are
removed. Malformed URLs produce a fixed placeholder; they never fall back to the
raw input. Keep credentials out of URL paths and plugin names. Request errors
retain their typed cause for `errors.Is` / `errors.As`, but their normal and `%+v`
rendering excludes arbitrary underlying text. Do not log the unwrapped cause.

Remote configuration uses a dedicated client rather than the legacy helper that
logged raw URLs and response bodies. It has a 30-second deadline, a 4 MiB decoded
response ceiling, and rejects redirects rather than forwarding credentials.
App/profile/label are escaped path components. The configured server base path
remains visible through the allowlisted URL representation; keep credentials out
of paths. Basic authentication and query parameters remain on the actual request
but are omitted from logs.
Failures report status or fixed categories, never response/configuration values.

These protections concern configuration and HTTP endpoint diagnostics. They do
not make user-supplied telemetry payloads safe to log, or redact credentials
embedded in arbitrary application message fields. Limit production debug logging
and treat historical logs from affected versions as potentially secret-bearing.
Rotate any credentials that were actually exposed and restrict retained log access.

The startup configuration dump in legacy revision
`0e568a13f4590a95917151800fac68bd1730554d` was removed in
[PR #34](https://github.com/Laisky/go-fluentd/pull/34). Rebuilding or reconfiguring
that old source does not include these protections. Qualify an immutable image
containing the fix before supplying alert credentials. JSON and YAML startup
regressions use synthetic credentials, and the alert-hook control sends only to
a local test server while verifying the real SDK payload.
