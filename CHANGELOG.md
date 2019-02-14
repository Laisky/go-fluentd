
*CURRENT*
---



*1.6.8*
---

- 2019-02-13 (Laisky) fix(paas-294): compatable to 0.9 journal data file 586e65def214861fc32ea519e8de935eea8d570b)

*1.6.7*
---

- 2019-02-11 (Laisky) build: upgrade go-utils

*1.6.6*
---

- 2019-02-01 (Laisky) fix: RegexNamedSubMatch should remove empty key 56375c50f03a102c1a644dc4b8755d9e2adb0518)
- 2019-02-01 (Laisky) docs: update readme
- 2019-02-01 (Laisky) ci: add ci var
- 2019-01-31 (Laisky) fix: remove empty key and replace '.' in key 97a0622993c08767d58796d2c163b456b9df7d05)
- 2019-01-29 (Laisky) test: add more test case

*1.6.5*
---

- 2019-01-30 (Laisky) fix(paas-287): should set payload to nil before decode fluentd's msg d0e346f6ca3288e0669b92adfe2de7cc13671fce)
- 2019-01-30 (Laisky) fix: add more logs

*1.6.4*
---

- 2019-01-29 (Laisky) fix: add `max_allowed_ahead_sec`

*1.6.3*
---

- 2019-01-29 (Laisky) fix: fluentd producer tags should not append env a2449487f3fe32b6f039815e93a7b079312cafc8)

*1.6.2*
---

- 2019-01-25 (Laisky) docs: update CHANGELOG
- 2019-01-25 (Laisky) perf: upgrade zap
- 2019-01-25 (Laisky) build: upgrade go-utils
- 2019-01-25 (Laisky) perf: use uniform clock in go-utils 34506fdf134a13cabb10a83743b485d30b5af95d)
- 2019-01-24 (Laisky) fix(paas-284): reduce debug log to reduce cpu 8462e49400488a5ab018981db5db9c692ba85077)
- 2019-01-24 (Laisky) fix: remove useless pprof entrypoint 5b5dc4e1764c22dab1601378af8a6639c2e6ce04)
- 2019-01-24 (Laisky) fix: check timer's ts
- 2019-01-24 (Laisky) build: upgrade iris
- 2019-01-24 (Laisky) perf: reduce invoke time.Now
- 2019-01-24 (Laisky) build: upgrade golang v1.11.5
- 2019-01-24 (Laisky) docs: update docs
- 2019-01-23 (Laisky) fix(paas-282): some bugs during testing b45f15cbb3aa85f17f65d3067a1ba32a577c05a5)
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
- 2019-01-15 (Laisky) feat(paas-276): able to load configuration from config-server 1bfb162f208fb927ad732d44a628e0f0a1368e75)
- 2019-01-14 (Laisky) feat(paas-275): refactory controllor - parse settings more flexible b232c92187d2af02331a5841b4487608cf5e5658)

*1.6*
---

- 2019-01-11 (Laisky) build: upgrade go-utils
- 2019-01-10 (Laisky) fix: flatten delimiter
- 2019-01-10 (Laisky) feat: flatten httprecv json
- 2019-01-10 (Laisky) fix: no not panic when got incorrect fluentd msg 269b6709fa888cc1a3b8ec3072e778017dfc8659)
- 2019-01-10 (Laisky) fix(paas-272): do not retry all es msgs when part of mesaages got error 6b9ac20d694c7575de9998bf1cf5ad173f300c3a)
- 2019-01-10 (Laisky) fix: add more err log
- 2019-01-10 (Laisky) style: less hardcoding
- 2019-01-10 (Laisky) feat(paas-270): add signature in httprecv d849ebf395dcd97df26c8cded3c2d767a34797ef)
- 2019-01-09 (Laisky) feat(paas-259): support external log api 1d2b8555118d82757e288dbeeca2c581777bfe14)
- 2019-01-09 (Laisky) build: upgrade glide & marathon
- 2019-01-07 (Laisky) feat(paas-261): add http json recv 872be3b69477703a74b18f25c87361b95fe39d74)
- 2019-01-09 (Laisky) feat(paas-265): use stretch to mount mfs af9230c7c027dbef03c452c5cbc89197b764f149)

*1.5.4*
---

- 2019-01-07 (Laisky) fix(paas-258): backup logs missing time 1826a82330e485421568439ea7e24c30327a3b0e)
- 2019-01-02 (Laisky) docs: update changelog

*1.5.3*
---

- 2018-12-27 (Laisky) feat(paas-249): set default_field `message` default to "" d5f3db2ad426444eb29eb5f5bb67d53ef7609fb2)
- 2018-12-26 (Laisky) fix: add panic logging in goroutines 1b4f9db49352fc40e159041028db34b77270a33a)
- 2018-12-26 (Laisky) docs: update changelog

*1.5.2*
---

- 2018-12-25 (Laisky) fix(paas-245): esdispatcher error c2407063c8e182cc941131199109711c5a83989c)
- 2018-12-25 (Laisky) fix(paas-246): journal data should rewrite into journal ed8453cb3c74ad2fcd125588d10554adebf6f907)
- 2018-12-25 (Laisky) perf(paas-245): remove kafka buffer c5176c39c71dc0d48a9245a629b90955cb8f5ec6)

*1.5.1*
---

- 2018-12-24 (Laisky) build: add http proxy
- 2018-12-24 (Laisky) fix(paas-244): upgrade go-utils

*1.5*
---

- 2018-12-24 (Laisky) feat(paas-212): async dispatcher
- 2018-12-21 (Laisky) fix(paas-212): some errors during testing 3be24c8b7f7fea16f5f4f2bb1962a8745b40a052)
- 2018-12-19 (Laisky) Revert "Revert "Merge branch 'feature/paas-212-fluent-buf' into develop"" f4231241dc987c2ae3898569e88b3e1d9518c433)
- 2018-12-19 (Laisky) Revert "Merge branch 'feature/paas-212-fluent-buf' into develop" e623dd44a22edd7f1de65afcf716c72c0bcdbd70)
- 2018-12-18 (Laisky) feat(paas-212): replace fluentd-buf completely 60988018a8122bbfde79ada03c5b4f6eee6d1f2f)

*1.4.2*
---

- 2018-12-12 (Laisky) fix: upgrade go-syslog to fix bug a8ec839633125075330e10e5dba4b73ef44c5ca3)
- 2018-12-12 (Laisky) feat(paas-225): support BLB health check f49b1ab5c0853348de19c554b96e908cf8429ea0)
- 2018-12-11 (Laisky) style: clean code

*1.4.1*
---

- 2018-12-12 (Laisky) fix(paas-231): trim blank in fields 87c0603880cb427ade4f43c4fe876854e14077f5)

*1.4*
---

- 2018-12-11 (Laisky) fix: do not send geely backup in go-fluentd now af4d422014506eefca64c188904d76177401a8fd)
- 2018-12-06 (Laisky) fix: do not retry when sender is too busy a7b88c14648709a3f2d5e3387294be32b71989de)
- 2018-12-06 (Laisky) fix(paas-222): race in dispatcher d514bb11d386f4fecea557a92b739ae4ab1c0b2c)
- 2018-12-06 (Laisky) fix: kafka_recvs config
- 2018-12-06 (Laisky) fix: typo
- 2018-12-06 (Laisky) fix: typo
- 2018-12-06 (Laisky) feat(paas-220): support rsyslog recv 826abe8d9a5e9fa84f812713c6200efe76e190dc)
- 2018-12-04 (Laisky) fix(paas-208): change flatten delimiter to "__" 1b8b9e39d81110c5bd61b61674b0995318fe2a3d)
- 2018-12-04 (Laisky) build: rename to `pateo.com`
