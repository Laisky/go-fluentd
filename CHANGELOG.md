
*CURRENT*
---



*v1.8.6*
---

- 2019-05-23 (Laisky) perf: reduce alloc in es sender
- 2019-05-22 (Laisky) ci: upgrade golang to 1.12.5
- 2019-05-22 (Laisky) perf: only allow one rotate waiting
- 2019-05-22 (Laisky) perf: only load all ids once
- 2019-05-22 (Laisky) fix: upgrade go-utils to v1.3.1

*1.8.5*
---

- 2019-05-15 (Laisky) docs: update readme
- 2019-05-14 (Laisky) ci: improve docker cache
- 2019-05-08 (Laisky) feat(paas-344): add prometheus metrics at `/metrics`

*1.8.4*
---

- 2019-04-26 (Laisky) build: no neee do `go mod download`
- 2019-04-26 (Laisky) perf: improve `runLB`

*1.8.3*
---

- 2019-04-17 (Laisky) build: fix bin file path
- 2019-04-17 (Laisky) fix: change ci to gomod
- 2019-04-17 (Laisky) build: replace glide by gomod in dockerfile
- 2019-04-17 (Laisky) build: replace glide by gomod
- 2019-04-16 (Laisky) docs: fix dockerfiles

*1.8.2*
---

- 2019-04-12 (Laisky) fix: ts format only support `.`
- 2019-04-12 (Laisky) fix: dispatch lock corner bug
- 2019-04-12 (Laisky) docs: add dockerfile relations

*1.8.1*
---

- 2019-04-09 (Laisky) fix(paas-320): dispatcher conflict with inChanForEachTag

*1.8*
---

- 2019-04-04 (Laisky) feat(paas-320): let dispatcher & tagpipeline parallel
- 2019-03-05 (Laisky) style: format improve

*1.7.2*
---

- 2019-03-04 (Laisky) docs: update changelog
- 2019-03-04 (Laisky) feat(paas-312): postfilter support plugins
- 2019-03-01 (Laisky) docs: add push at docker doc
- 2019-03-01 (Laisky) build: upgrade to golang:1.12
- 2019-03-01 (Laisky) build: upgrade to golang:1.12
- 2019-03-01 (Laisky) build: upgrade go-utils to v9.1
- 2019-03-01 (Laisky) fix(paas-312): add some tests and fix some bugs
- 2019-02-28 (Laisky) docs: update changelog

*1.7.1*
---

- 2019-02-28 (Laisky) docs: fix settings demo
- 2019-02-28 (Laisky) docs: add quick start
- 2019-02-21 (Laisky) fix: more details in log
- 2019-02-21 (Laisky) feat(paas-288): add fluentd-forward log
- 2019-02-20 (Laisky) ci: add id into docker tag
- 2019-02-20 (Laisky) fix
- 2019-02-20 (Laisky) fix
- 2019-02-19 (Laisky) test: fix test readme
- 2019-02-15 (Laisky) ci: fix test
- 2019-02-15 (Laisky) ci: fix test
- 2019-02-15 (Laisky) ci: fix test
- 2019-02-15 (Laisky) ci: add test
- 2019-02-15 (Laisky) docs: update changelog

*1.7*
---

- 2019-02-15 (Laisky) build: upgrade to alpine3.9
- 2019-02-14 (Laisky) perf: reduce memory usage in recv
- 2019-02-14 (Laisky) ci: add ci retry
- 2019-02-14 (Laisky) perf: replace all encoding/json
- 2019-02-13 (Laisky) feat(paas-294): replace codec by msgp
- 2019-02-12 (Laisky) perf: replace builtin json by `github.com/json-iterator/go` in recvs
- 2019-02-12 (Laisky) perf: replace builtin json by `github.com/json-iterator/go`
- 2019-02-12 (Laisky) style: fix some format

*1.6.8*
---

- 2019-02-13 (Laisky) fix(paas-294): compatable to 0.9 journal data file

*1.6.7*
---

- 2019-02-11 (Laisky) build: upgrade go-utils

*1.6.6*
---

- 2019-02-01 (Laisky) fix: RegexNamedSubMatch should remove empty key
- 2019-02-01 (Laisky) docs: update readme
- 2019-02-01 (Laisky) ci: add ci var
- 2019-01-31 (Laisky) fix: remove empty key and replace '.' in key
- 2019-01-29 (Laisky) test: add more test case

*1.6.5*
---

- 2019-01-30 (Laisky) fix(paas-287): should set payload to nil before decode fluentd's msg
- 2019-01-30 (Laisky) fix: add more logs

*1.6.4*
---

- 2019-01-29 (Laisky) fix: add `max_allowed_ahead_sec`

*1.6.3*
---

- 2019-01-29 (Laisky) fix: fluentd producer tags should not append env

*1.6.2*
---

- 2019-01-25 (Laisky) docs: update CHANGELOG
- 2019-01-25 (Laisky) perf: upgrade zap
- 2019-01-25 (Laisky) build: upgrade go-utils
- 2019-01-25 (Laisky) perf: use uniform clock in go-utils
- 2019-01-24 (Laisky) fix(paas-284): reduce debug log to reduce cpu
- 2019-01-24 (Laisky) fix: remove useless pprof entrypoint
- 2019-01-24 (Laisky) fix: check timer's ts
- 2019-01-24 (Laisky) build: upgrade iris
- 2019-01-24 (Laisky) perf: reduce invoke time.Now
- 2019-01-24 (Laisky) build: upgrade golang v1.11.5
- 2019-01-24 (Laisky) docs: update docs
- 2019-01-23 (Laisky) fix(paas-282): some bugs during testing
- 2019-01-23 (Laisky) fix: fluentd sender missing env
- 2019-01-17 (Laisky) docs: update docs

*1.6.1*
---

- 2019-01-17 (Laisky) fix: `append_time_zone`
- 2019-01-17 (Laisky) fix: kafka recv error
- 2019-01-17 (Laisky) docs: add docker readme
- 2019-01-17 (Laisky) docs: add pipeline label
- 2019-01-17 (Laisky) ci: `IMAGE_NAME` in gitlab-ci
- 2019-01-17 (Laisky) ci: fix image bug
- 2019-01-16 (Laisky) ci: fix image bug
- 2019-01-16 (Laisky) ci: add proxy
- 2019-01-16 (Laisky) ci: add golang-stretch
- 2019-01-16 (Laisky) ci: enable marathon
- 2019-01-16 (Laisky) ci: add strecth-mfs
- 2019-01-16 (Laisky) ci: fix ci yml
- 2019-01-16 (Laisky) fix(paas-281): add gitlab-ci
- 2019-01-15 (Laisky) docs: update TOC
- 2019-01-15 (Laisky) docs: add cn doc
- 2019-01-15 (Laisky) style: rename dockerfile
- 2019-01-15 (Laisky) feat(paas-276): able to load configuration from config-server
- 2019-01-14 (Laisky) feat(paas-275): refactory controllor - parse settings more flexible

*1.6*
---

- 2019-01-11 (Laisky) build: upgrade go-utils
- 2019-01-10 (Laisky) fix: flatten delimiter
- 2019-01-10 (Laisky) feat: flatten httprecv json
- 2019-01-10 (Laisky) fix: no not panic when got incorrect fluentd msg
- 2019-01-10 (Laisky) fix(paas-272): do not retry all es msgs when part of mesaages got error
- 2019-01-10 (Laisky) fix: add more err log
- 2019-01-10 (Laisky) style: less hardcoding
- 2019-01-10 (Laisky) feat(paas-270): add signature in httprecv
- 2019-01-09 (Laisky) feat(paas-259): support external log api
- 2019-01-09 (Laisky) build: upgrade glide & marathon
- 2019-01-07 (Laisky) feat(paas-261): add http json recv
- 2019-01-09 (Laisky) feat(paas-265): use stretch to mount mfs

*1.5.4*
---

- 2019-01-07 (Laisky) fix(paas-258): backup logs missing time
- 2019-01-02 (Laisky) docs: update changelog

*1.5.3*
---

- 2018-12-27 (Laisky) feat(paas-249): set default_field `message` default to ""
- 2018-12-26 (Laisky) fix: add panic logging in goroutines
- 2018-12-26 (Laisky) docs: update changelog

*1.5.2*
---

- 2018-12-25 (Laisky) fix(paas-245): esdispatcher error
- 2018-12-25 (Laisky) fix(paas-246): journal data should rewrite into journal
- 2018-12-25 (Laisky) perf(paas-245): remove kafka buffer

*1.5.1*
---

- 2018-12-24 (Laisky) build: add http proxy
- 2018-12-24 (Laisky) fix(paas-244): upgrade go-utils

*1.5*
---

- 2018-12-24 (Laisky) feat(paas-212): async dispatcher
- 2018-12-21 (Laisky) fix(paas-212): some errors during testing
- 2018-12-19 (Laisky) Revert "Revert "Merge branch 'feature/paas-212-fluent-buf' into develop""
- 2018-12-19 (Laisky) Revert "Merge branch 'feature/paas-212-fluent-buf' into develop"
- 2018-12-18 (Laisky) feat(paas-212): replace fluentd-buf completely

*1.4.2*
---

- 2018-12-12 (Laisky) fix: upgrade go-syslog to fix bug
- 2018-12-12 (Laisky) feat(paas-225): support BLB health check
- 2018-12-11 (Laisky) style: clean code

*1.4.1*
---

- 2018-12-12 (Laisky) fix(paas-231): trim blank in fields

*1.4*
---

- 2018-12-11 (Laisky) fix: do not send geely backup in go-fluentd now
- 2018-12-06 (Laisky) fix: do not retry when sender is too busy
- 2018-12-06 (Laisky) fix(paas-222): race in dispatcher
- 2018-12-06 (Laisky) fix: kafka_recvs config
- 2018-12-06 (Laisky) fix: typo
- 2018-12-06 (Laisky) fix: typo
- 2018-12-06 (Laisky) feat(paas-220): support rsyslog recv
- 2018-12-04 (Laisky) fix(paas-208): change flatten delimiter to "__"
- 2018-12-04 (Laisky) build: rename to `pateo.com`
