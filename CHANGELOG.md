       
*CURRENT*
---
    
    
       
*1.5.3*
---
    
- 2018-12-27 (Laisky) feat(paas-249): set default_field `message` default to "" -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d5f3db2ad426444eb29eb5f5bb67d53ef7609fb2)
- 2018-12-26 (Laisky) fix: add panic logging in goroutines -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1b4f9db49352fc40e159041028db34b77270a33a)
- 2018-12-26 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b32483fff37ea78ffb374c9e44d19de58dd8b863)    
       
*1.5.2*
---
    
- 2018-12-25 (Laisky) fix(paas-245): esdispatcher error -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c2407063c8e182cc941131199109711c5a83989c)
- 2018-12-25 (Laisky) fix(paas-246): journal data should rewrite into journal -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/ed8453cb3c74ad2fcd125588d10554adebf6f907)
- 2018-12-25 (Laisky) perf(paas-245): remove kafka buffer -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c5176c39c71dc0d48a9245a629b90955cb8f5ec6)    
       
*1.5.1*
---
    
- 2018-12-24 (Laisky) build: add http proxy -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/67282982a91ab3146c7f84f81fd1854cfb8e372b)
- 2018-12-24 (Laisky) fix(paas-244): upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e9be92a3fbadfb90bd11b8cd722b6150c348a41e)    
       
*1.5*
---
    
- 2018-12-24 (Laisky) feat(paas-212): async dispatcher -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a4d8ddb29555eb4a6c9684f5c2d96f1ef4a7c513)
- 2018-12-21 (Laisky) fix(paas-212): some errors during testing -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/3be24c8b7f7fea16f5f4f2bb1962a8745b40a052)
- 2018-12-19 (Laisky) Revert "Revert "Merge branch 'feature/paas-212-fluent-buf' into develop"" -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/f4231241dc987c2ae3898569e88b3e1d9518c433)
- 2018-12-19 (Laisky) Revert "Merge branch 'feature/paas-212-fluent-buf' into develop" -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e623dd44a22edd7f1de65afcf716c72c0bcdbd70)
- 2018-12-18 (Laisky) feat(paas-212): replace fluentd-buf completely -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/60988018a8122bbfde79ada03c5b4f6eee6d1f2f)    
       
*1.4.2*
---
    
- 2018-12-12 (Laisky) fix: upgrade go-syslog to fix bug -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a8ec839633125075330e10e5dba4b73ef44c5ca3)
- 2018-12-12 (Laisky) feat(paas-225): support BLB health check -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/f49b1ab5c0853348de19c554b96e908cf8429ea0)
- 2018-12-11 (Laisky) style: clean code -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a02822eba35c178110e505643b8749ce8cc793d7)    
       
*1.4.1*
---
    
- 2018-12-12 (Laisky) fix(paas-231): trim blank in fields -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/87c0603880cb427ade4f43c4fe876854e14077f5)    
       
*1.4*
---
    
- 2018-12-11 (Laisky) fix: do not send geely backup in go-fluentd now -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/af4d422014506eefca64c188904d76177401a8fd)
- 2018-12-06 (Laisky) fix: do not retry when sender is too busy -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a7b88c14648709a3f2d5e3387294be32b71989de)
- 2018-12-06 (Laisky) fix(paas-222): race in dispatcher -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d514bb11d386f4fecea557a92b739ae4ab1c0b2c)
- 2018-12-06 (Laisky) fix: kafka_recvs config -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/f1d69ef2f1c029d7215d11b72b8828bd19b748c3)
- 2018-12-06 (Laisky) fix: typo -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e0f057bee8025ff1c0c7c494f5adc4eb0a63b201)
- 2018-12-06 (Laisky) fix: typo -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/124e9d0d9c548283ac3832ccb3c528b7e65083f4)
- 2018-12-06 (Laisky) feat(paas-220): support rsyslog recv -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/826abe8d9a5e9fa84f812713c6200efe76e190dc)
- 2018-12-04 (Laisky) fix(paas-208): change flatten delimiter to "__" -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1b8b9e39d81110c5bd61b61674b0995318fe2a3d)
- 2018-12-04 (Laisky) build: rename to `pateo.com` -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/74f7aceaac23372a39136ae375cc733b86161c22)    
