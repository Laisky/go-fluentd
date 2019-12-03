       
*CURRENT*
---
    
- 2019-10-18 (Laisky) ci: upgrade golang v1.13.3 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/6a5a9492c1ae63a4d3572f1e6e3580b1094b9b84)
- 2019-10-18 (Laisky) fix: upgrade go-utils v1.8.1 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e84ed4a85251f366e6ed9ff19de368d7db7ea4a4)    
       
*v1.11.5*
---
    
- 2019-10-16 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e423555cf033ce217273c04d500abcc68ec565f2)
- 2019-10-16 (Laisky) perf: tidy -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c84b152ad11aedbd5f6624d8374178097de11943)
- 2019-10-15 (Laisky) test: fix ci lint -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/91f4ee5910bf6051e322a0e90e5b43d51a2d7ca2)
- 2019-10-09 (Laisky) docs: update example settings -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d62c4454aab36e9d7565ded71cd5a1f0828b70c8)
- 2019-10-09 (Laisky) fix: fluentd decodeMsg with context -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/6ce3e4ab0bdbf1db50c7ad26d506a6b358a06c8b)    
       
*v1.11.4*
---
    
- 2019-10-08 (Laisky) fix(paas-420): - journal og ttl - duplicate id - kafka -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/95bb1cc5ab87ed7fa2c966392f363aa9deb8349d)
- 2019-09-30 (Laisky) fix: upgrade go-utils v1.7.8 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/9c6c58ee4d8c7b3a41485b2708a2b9207d207a1a)
- 2019-09-30 (Laisky) fix: rotate counter start at 1 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/6c7f40a4747a7b47018a63a98b2878d53b78914d)
- 2019-09-30 (Laisky) fix: fix duplicate in counter -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/ac6c0d37b413e47aa8bdca6bc13a2d0ebd08d8fb)
- 2019-09-29 (Laisky) fix(paas-408): keep one legacy buf file to descend duplicate' -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/0a971159f45d0554bc1d25d782a06eb6e1c9cf8a)
- 2019-09-29 (Laisky) fix: use normal counter to fix duplicate -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/ee350cb6f4a83d8b45a056ced690172c6b313926)
- 2019-09-23 (Laisky) build: upgrade go-utils v1.7.7 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d78e418d4fe0a6f5bbeebd2cf63430a67ff3255f)
- 2019-09-18 (Laisky) build: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/fb3ef755ec943059e56f84fc8da2808307e1a5c0)
- 2019-09-16 (Laisky) build: go mod -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1d759b8acb941f38bd6fa46c7dc87d45c70f652d)
- 2019-09-16 (Laisky) feat(paas-412): support k8s log -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d312653f3110ed0d5e8f29cf1d4bc2b8f2e36dce)
- 2019-09-12 (Laisky) feat: preallocate file size in journal -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b5592093dc1d9d27dbfb61edfe07d42b3e4fb436)    
       
*v1.11.1*
---
    
- 2019-09-06 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/4672776611bc7da1eb23ae503323dd379ab438f5)
- 2019-09-06 (Laisky) ci: upgrade go-utils v1.7.6 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/96b38fac8491915a0d8e6b2f1abea6bb448d802c)
- 2019-09-05 (Laisky) fix: refact contronllor's ctx -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/4fc7ba56ff66dd7f6e4fea2eb278aa53fa394fd2)
- 2019-09-05 (Laisky) fix: use stopChan replace cancel -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/5101d6ee4bafacacd982f04239614df2f8653d9c)
- 2019-09-05 (Laisky) fix: kafka recv after closed -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/2f5a0cefa68ef133f341506e1e93e384381a5f9c)    
       
*v1.11.0*
---
    
- 2019-09-05 (Laisky) perf: reduce goroutines -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/0176c82ef75f8aca1dc0da3fd7f598de57264b2f)
- 2019-09-05 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b746aeacbf2a760f020e5bbd0af3665fbdf125f4)
- 2019-09-05 (Laisky) ci: upgrade go-utils v1.7.5 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/6d7d6cf42fa4cc26fde8f50311d9f7b6939c4f0c)
- 2019-09-05 (Laisky) fix: shrink deps -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/0aa8e68e5b0b78624bebeec4276e0c08fc992b46)
- 2019-09-05 (Laisky) ci: upgrade go-utils v1.7.4 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/81474c7a52575b576b0c363afa9d553e3d1c7724)
- 2019-09-04 (Laisky) fix: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/9736cff20ebe3dc8d1c7980c2941db54bfcf8160)
- 2019-09-04 (Laisky) fix: add context in postpipeline -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d7b806f9b71e6f265869f92e9a3592a334843d37)
- 2019-09-04 (Laisky) build: upgrade go v1.13.0 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/974817c3c871396ba3a8372f2bc25c21961e8c46)
- 2019-09-04 (Laisky) fix: add context in accptor -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/4592dfb64622466ea39cb10c5f67cd6047065706)
- 2019-09-04 (Laisky) fix: add context in acceptorPipeline -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/76fccd773a4af35c8224b2fc7fb394521e585bd4)
- 2019-09-04 (Laisky) fix: add context in producer -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/979176bd356d24f8759633c94856a871e4231187)
- 2019-09-04 (Laisky) fix: refactor tagPipeline -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a037f91267c0c9477236d4858145018403a7a9d6)
- 2019-09-03 (Laisky) ci: upgrade go-utils v1.7.3 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/ba239a7971d61e67d5d25a05adaea99310eb7503)
- 2019-09-03 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/fe95374c0b035bc5602637d497ecc47d964f9ab1)    
       
*v1.10.8*
---
    
- 2019-09-03 (Laisky) fix: ctx.Done return -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/2cb7f418d65762bd91fe625303672a69fa2d3180)    
       
*v1.10.7*
---
    
- 2019-09-02 (Laisky) ci: upgrade go-utils v1.7.0 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/5557828e2c3f3b00b05a1b892db6ce9c7b8daa35)
- 2019-09-02 (Laisky) feat: add context to control journal -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/6d0de3b1ad4e34bba0ebd676956620c8aecd5754)
- 2019-09-02 (Laisky) feat(paas-405): upgrade go-utils, use new ids set -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/8e677e731eb52a254487e5138cc158a38ef3b0fa)
- 2019-08-29 (Laisky) docs: update example settings -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a2791f8fdfa99520caed9c123f083de682d46ba4)    
       
*v1.10.6*
---
    
- 2019-08-29 (Laisky) fix: blocking when commitChan is full -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/99eb7f583e8ad536022531681922ef7463a961ac)    
       
*v1.10.5*
---
    
- 2019-08-28 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/09d79ef45a9ab7f40e742d0b7f3d41706ee2a788)
- 2019-08-28 (Laisky) fix: commit chan should not discard -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/eb71661a7f2a6ad989d80fb3d874de86fcf76bfd)
- 2019-08-28 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/8b9394d09e17a5489cefe329d3851c39464785aa)
- 2019-08-28 (Laisky) perf: periodic gc -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/0d42bfb06f8ba77df147492109c01acc9f479508)    
       
*v1.10.4*
---
    
- 2019-08-28 (Laisky) perf: disable jj gc -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/417c99a5581bfa652017a58bcb57aebd072a30ff)    
       
*v1.10.3*
---
    
- 2019-08-28 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/dbe4979ef9bf1dfacfb71fa03aacd9bd40c47b52)
- 2019-08-28 (Laisky) ci: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/bf3efbb304bdd1eac117c2a40853c35c7680be54)
- 2019-08-27 (Laisky) fix(paas-403): journal roll bug -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/122f049294cbfc6ac71bbdd28996856caf1434b9)
- 2019-08-27 (Laisky) build: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/790fe0ef02d16866881e2e105f32e2743fcd303b)
- 2019-08-27 (Laisky) build: `http_proxy` should in lowercase -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d9468a0d277c454eefa5a5a73379df6385cc1f62)
- 2019-08-27 (Laisky) fix: upgrade to go v1.12.9 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/ea689b15eb2e274e7a74953e58e7aa1e22db29bf)
- 2019-08-27 (Laisky) fix: upgrade to go v1.12.9 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a660a445ff864ca5e6ca52144bfeac4ea8a21a20)
- 2019-08-27 (Laisky) perf: use `NewMonotonicCounterFromN` -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/0af184d675be2b435da8e4b2fec8986d7fc3e0f3)
- 2019-08-26 (Laisky) feat(paas-398): split journal into different directory by tag -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/6d2a910f672dd2448b3f98dc8cae6c7a6957f19b)
- 2019-08-23 (Laisky) fix(paas-397): msg disorder after acceptorPipeline, then cause concator error -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/943dca4f5e70f1ce47abe59632bfcf8ef236588f)    
       
*v1.10.2*
---
    
- 2019-08-21 (Laisky) build: upgrade go-utils to v1.6.2 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/637c90e603312fd5fbc25ee79e0d9521399ad134)
- 2019-08-21 (Laisky) fix: format warn -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/9a7a83a47e8091ea7e89bfbe366c433d7b02f34b)
- 2019-08-21 (Laisky) fix(paas-397): missing content -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/540721054551ebb38a07ccc1cc95a9b3727bb6ae)    
       
*v1.10.1*
---
    
- 2019-08-20 (Laisky) fix: ignore es conflict error -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e70597371e26b0152e13db5badab7d8a69421da5)    
       
*v1.10.0*
---
    
- 2019-08-20 (Laisky) ci: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/0dc1e192752232b2c83bd79918a8be25291df029)
- 2019-08-20 (Laisky) ci: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/5cdf4817a46e33a80a94819e6578e3d75dd024fd)
- 2019-08-19 (Laisky) docs: update quickstart -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/aa87fe6e1728de2d8f94f521366a7b2f637e0665)
- 2019-08-19 (Laisky) feat: enable gz in journal -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d55eb9c59e940911e4508ecd9a276f64907e6c9a)
- 2019-08-19 (Laisky) style: more log -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/df2f1882463d76c6fd93a24ed1072f9b614b0bf6)    
       
*v1.9.3*
---
    
- 2019-08-15 (Laisky) build: upgrade go-utils to v1.5.4 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e2ad13db5acd8c93415654f31b50d874bb2c2fd7)
- 2019-08-15 (Laisky) fix: double default postfilter -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c6c10f0cd5d852f014bcdad26db42c8ec85757c1)
- 2019-08-14 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/64e47e699d950bdcf36e9333d176121fbdc02289)
- 2019-08-14 (Laisky) feat: support `@RANDOM_STRING` -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/f1b814ca2225235fce95e74a1f8294baf6c210cf)
- 2019-08-14 (Laisky) build: upgrade to go 1.12.7 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b4cdca172abe2ff48a843119d23c3e773f5fb255)
- 2019-08-14 (Laisky) feat(paas-390): add wuling mapping -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c8f2e952b7fb3a150d73c48c97e43386c62ce9f7)
- 2019-07-23 (Laisky) perf: upgrade go-utils to v1.5.3 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/774b48c4f62317c143820d9dc8734c7764218a19)
- 2019-07-17 (Laisky) fix: optimize regexp -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/4baa243c92b689312eece047762b6a598c800eea)
- 2019-07-17 (Laisky) docs: update example settings -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/45b4398f8f0281eb33f5d66be48ae0e8ebb07b17)
- 2019-06-26 (Laisky) build: upgrade go-utils to v1.5.1 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a5c1debfa499f0aa6dc399fb5572ae0cc24cc6b5)
- 2019-06-26 (Laisky) fix: go mod conflict -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/ae7b84eeef5c61320d6e75d865023861c69e1039)
- 2019-06-21 (Laisky) build: upgrade to golang:1.12.6 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/fafff03e0b6411bf734880e9a3e2638e5ed11e8d)
- 2019-06-12 (Laisky) ci: disable vendor cache -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/935ba575cc472e201e2ebbd9e34a01ec4d5aa696)
- 2019-06-12 (Laisky) test: fix test docket -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/410d6711a0b8799a5916a28e40ca1a044b2cf816)    
       
*v1.9.1*
---
    
- 2019-06-12 (Laisky) test: fix test docket -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/ce8283711c94e4f64d0bd7ab3d3be1bd85ce9092)
- 2019-06-12 (Laisky) test: fix test docket -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/87564dca8b29c429176c32b217c6ac8de0754296)
- 2019-06-12 (Laisky) ci: update dockerfile -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b69819a5c46a359a5eb35ee722cf3fe125681d04)
- 2019-06-12 (Laisky) ci: update dockerfile -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/820c0e59273c9e18312df2d16f29a9c49d49e9f4)
- 2019-06-12 (Laisky) docs: update readme -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/66fabe6cb96289b56e95928e20f523174d61f04f)
- 2019-06-12 (Laisky) build: fix package conflict -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/759fc607ef8463b47a91974c43d0a386e0264696)
- 2019-06-12 (Laisky) ci: update travis -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/481cc435a0bb2f460bfc96497a48436cb474cec5)
- 2019-06-12 (Laisky) ci: update cache -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/385f0ae6b4cd70875df76a04defea3f0e6f33fe0)
- 2019-06-10 (Laisky) ci: fix dependencies error -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/0c7fd76d95006c48aaf67bb8a1b046e1bfc3b442)
- 2019-06-10 (Laisky) fix: add uint32set & improve rotate -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b7b2a0357a0f8fbe7eb1ee1c71f77b46a452d957)
- 2019-06-04 (Laisky) docs: update readme -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/980dd73599e3c41ebfa3a20f879d4f6f2a0397f4)    
       
*v1.9.0*
---
    
- 2019-06-04 (Laisky) style: add some comment -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/73374b34e25637f7f327a159bbf8a7da0f9346cc)
- 2019-06-04 (Laisky) ci: add prometheus -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/13a36750e4d9e14640233cd488d4c03171d170b8)
- 2019-06-04 (Laisky) ci: upgrade go-utils v1.4.0 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/9931fed4a767ba5e317afee154dad89c31de8ab7)
- 2019-06-04 (Laisky) fix(paas-361): replace iris by gin -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/7839146cff6de60427a8b5cd5619acb65dfd3dc5)
- 2019-05-31 (Laisky) fix(paas-360): corrupt file panic -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/f031cb7d2e2de7649ab1558ac2f834ab0fd86323)
- 2019-05-31 (Laisky) ci: upgrade go-utils v1.3.8 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c07b7d449c2a7af63c1136440bcb3afd6f3832f5)
- 2019-05-31 (Laisky) fix(paas-357): reduce memory use -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c9765a57509aaea381ce70a118715b0190ed1a31)
- 2019-05-29 (Laisky) ci: upgrade go-utils v1.3.6 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/bd2c3ac39468c72f76b9a6409f1290b7ed8c1de7)
- 2019-05-29 (Laisky) docs: add comment -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/f2802495b70f2f0b0fd2e720f10e1aa4ec0947ae)    
       
*v1.8.9*
---
    
- 2019-05-29 (Laisky) ci: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/769399826d907681ed0233358f8ef9a77e6c3a1c)
- 2019-05-29 (Laisky) fix: compatable with smaller rotate id -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a3407cb506d1dfa47c6a43a55f6caa53411a6775)
- 2019-05-28 (Laisky) docs: update readme -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/330eb020f140157850c6466ffc869bc58fcab038)
- 2019-05-28 (Laisky) fix: recvs check active env -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/7dd9c57fed75e9a03daf50ef5c5c3f07da470648)
- 2019-05-28 (Laisky) feat: add null sender -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/8a7f815dad4f2be3e1902f944241cb2c584e73e0)
- 2019-05-28 (Laisky) +null -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/da1d18dc40e59bccc3ebfc060b39974575b150fd)
- 2019-05-27 (Laisky) ci: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/998ed8c675f59866e55e65789ce6fe63994576e4)
- 2019-05-27 (Laisky) docs: update readme -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c8f1ccb02bc1d9b390e679f2aa2c2791d8a97902)    
       
*v1.8.8*
---
    
- 2019-05-24 (Laisky) fix(paas-357): journal memory leak -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/41cd453f1f6e8f2b941d2d8f8261ba0c3f234ccb)    
       
*v1.8.7*
---
    
- 2019-05-23 (Laisky) ci: update gomod -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d2c9fd4d6f5dcffde7068122c15c6cbbfc73f3be)
- 2019-05-23 (Laisky) docs: update readme -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/3d8fec67d6188c5e0d628e19ac1392a2caa5029f)    
       
*v1.8.6*
---
    
- 2019-05-23 (Laisky) perf: reduce alloc in es sender -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/811298b8d1c4743b8b0eb2286f8ccaa62a30f2cb)
- 2019-05-22 (Laisky) ci: upgrade golang to 1.12.5 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a667d7d2bb3863d853d6df087bb50ea00f8d04ee)
- 2019-05-22 (Laisky) perf: only allow one rotate waiting -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/13dab90b560f05b0df2f0450c12a44ef3feadd99)
- 2019-05-22 (Laisky) perf: only load all ids once -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e597a7c4433e3841156a417b87ef9a47240ed34f)
- 2019-05-22 (Laisky) fix: upgrade go-utils to v1.3.1 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1f36982774d56c5b4c5a76afadea5785f08fabc6)    
       
*1.8.5*
---
    
- 2019-05-15 (Laisky) docs: update readme -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c9ed2503019f8789577f2a77aedb2b479768bbe8)
- 2019-05-14 (Laisky) ci: improve docker cache -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/9b559dff247c0addd6000ce46a90105c0905e9fc)
- 2019-05-08 (Laisky) feat(paas-344): add prometheus metrics at `/metrics` -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/09973dd9688d9db0a86f51f4f04dbd8cae086649)    
       
*1.8.4*
---
    
- 2019-04-26 (Laisky) build: no neee do `go mod download` -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c57e314019cd4dc9a911b1e3e62494f53fa753ff)
- 2019-04-26 (Laisky) perf: improve `runLB` -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/91e96bb0a8584d2060e19a36c89c31503ddfcdcf)    
       
*1.8.3*
---
    
- 2019-04-17 (Laisky) build: fix bin file path -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/3580851729b5d54224c34ce33fc91252efd5cb26)
- 2019-04-17 (Laisky) fix: change ci to gomod -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/25b3d912aa1f1bfc765ed33a945fa42839406dff)
- 2019-04-17 (Laisky) build: replace glide by gomod in dockerfile -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/628e7b7db3a723555bdc9109e2b9e5ea9061c027)
- 2019-04-17 (Laisky) build: replace glide by gomod -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b1d24d7fb9684ec7f4b2324e56cbfa5e88bddbaf)
- 2019-04-16 (Laisky) docs: fix dockerfiles -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/275a4d95ed8ed3fb9d4205f74cb701162be69aa8)    
       
*1.8.2*
---
    
- 2019-04-12 (Laisky) fix: ts format only support `.` -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1c38d79d83c7fb073adef04dc48e95b32cf9df02)
- 2019-04-12 (Laisky) fix: dispatch lock corner bug -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/09df14654cda6bb9924ac5b52af71ec301552a8c)
- 2019-04-12 (Laisky) docs: add dockerfile relations -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1d66f37117d7cedf0203a4268111cec6869561b6)    
       
*1.8.1*
---
    
- 2019-04-09 (Laisky) fix(paas-320): dispatcher conflict with inChanForEachTag -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/abe26c13cc9dc15d1a431e67f519e5fd79637d67)    
       
*1.8*
---
    
- 2019-04-04 (Laisky) feat(paas-320): let dispatcher & tagpipeline parallel -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/80ac1420ffe15b0a24ba47518ddddcf918243fe9)
- 2019-03-05 (Laisky) style: format improve -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/9db421a51fe1208326c2242c3644c19100392fa6)    
       
*1.7.2*
---
    
- 2019-03-04 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/6151b366a9680c1e65d0ca66bcf8567ff7a856ee)
- 2019-03-04 (Laisky) feat(paas-312): postfilter support plugins -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/953696912fc1b00c77244d6d69f96666e2dc063a)
- 2019-03-01 (Laisky) docs: add push at docker doc -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/356f015d4ddbfb030170626675e21ffa11d93848)
- 2019-03-01 (Laisky) build: upgrade to golang:1.12 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b8b1d92ed00e00fe8efed1ed820ce03db47096ac)
- 2019-03-01 (Laisky) build: upgrade to golang:1.12 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/41c39ee0da1c3bcc2520f56416e14a8c9ec96196)
- 2019-03-01 (Laisky) build: upgrade go-utils to v9.1 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/854ab0ebf1d4c77971a686a91035ee48071ce262)
- 2019-03-01 (Laisky) fix(paas-312): add some tests and fix some bugs -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/7ac5700d7607b8f1154eb21b9897e9a1fd9a6206)
- 2019-02-28 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e3ba374f92fe5ad311098e0f5c0c1ab4a416dbb4)    
       
*1.7.1*
---
    
- 2019-02-28 (Laisky) docs: fix settings demo -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/cb371def614527753f19bf3306e4e62405836edd)
- 2019-02-28 (Laisky) docs: add quick start -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c1902ddca29b571a99c5421d2b0ff8b5293426ec)
- 2019-02-21 (Laisky) fix: more details in log -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/83d0bda6b37ab2e48cdadce5df49aee5c8587657)
- 2019-02-21 (Laisky) feat(paas-288): add fluentd-forward log -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/8dd71068c3a2fbcc1bcafefb4319a9fd4a141026)
- 2019-02-20 (Laisky) ci: add id into docker tag -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/58b6fb9c870b899aa95e59f6f10e325e9ee75993)
- 2019-02-20 (Laisky) fix -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/9c9cbdda90c7be4a021ebe5d89bfaaaa16d99b04)
- 2019-02-20 (Laisky) fix -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/8a73735c0178f7ef3a4cd7b11897df569f94b12d)
- 2019-02-19 (Laisky) test: fix test readme -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/467b5a9782c9a2ba626937f356978436d24fb255)
- 2019-02-15 (Laisky) ci: fix test -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/778f867a309484c3a9d099a3365fc4204c9f6f66)
- 2019-02-15 (Laisky) ci: fix test -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/6f2951ce89713991dd524265d406b4dd8b9fa158)
- 2019-02-15 (Laisky) ci: fix test -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1453a7f21f1d6a215676b8ce000d52bb56710b70)
- 2019-02-15 (Laisky) ci: add test -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/c5e82df7ca608375406269f7112b1cb199bb19d1)
- 2019-02-15 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/ebf681b8893f718c521c1b0deb5a7e93af86581b)    
       
*1.7*
---
    
- 2019-02-15 (Laisky) build: upgrade to alpine3.9 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d8385628f861d13c09b28029b8498fe29daac989)
- 2019-02-14 (Laisky) perf: reduce memory usage in recv -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d6d11d740a4f9a475c463e0941e8faa44f4c3b49)
- 2019-02-14 (Laisky) ci: add ci retry -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b062dcee54f8eecd0ce5fa8c9ff70d4ab4e8881a)
- 2019-02-14 (Laisky) perf: replace all encoding/json -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/09b2e510cc60d1a75ed73bfe518f44ca076f1536)
- 2019-02-13 (Laisky) feat(paas-294): replace codec by msgp -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/aaa94c083ab502e0ad0431af0ac2e6c273700c33)
- 2019-02-12 (Laisky) perf: replace builtin json by `github.com/json-iterator/go` in recvs -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/6b64d82007c0326547def9770ed16c258f135b47)
- 2019-02-12 (Laisky) perf: replace builtin json by `github.com/json-iterator/go` -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b950e0ab1423b181b24bf19ba46ae05f6746ae08)
- 2019-02-12 (Laisky) style: fix some format -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/11b1d500a03efa78f878a429a3f3719797013f49)    
       
*1.6.8*
---
    
- 2019-02-13 (Laisky) fix(paas-294): compatable to 0.9 journal data file -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/586e65def214861fc32ea519e8de935eea8d570b)    
       
*1.6.7*
---
    
- 2019-02-11 (Laisky) build: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/37b5ad68f25b6adcd32bc6d9dc0567ac5f4ab5ec)    
       
*1.6.6*
---
    
- 2019-02-01 (Laisky) fix: RegexNamedSubMatch should remove empty key -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/56375c50f03a102c1a644dc4b8755d9e2adb0518)
- 2019-02-01 (Laisky) docs: update readme -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/215684fad2c49b59951aeb1e632213143bfc3df2)
- 2019-02-01 (Laisky) ci: add ci var -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/555a148568d0d65d7ccd137b5d2b0a8ce022febd)
- 2019-01-31 (Laisky) fix: remove empty key and replace '.' in key -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/97a0622993c08767d58796d2c163b456b9df7d05)
- 2019-01-29 (Laisky) test: add more test case -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/faf5a0170b5fc8869bba6c69690cb90db476f4b0)    
       
*1.6.5*
---
    
- 2019-01-30 (Laisky) fix(paas-287): should set payload to nil before decode fluentd's msg -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d0e346f6ca3288e0669b92adfe2de7cc13671fce)
- 2019-01-30 (Laisky) fix: add more logs -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/2922ad536df3c856b9530578002e6ae40459a01d)    
       
*1.6.4*
---
    
- 2019-01-29 (Laisky) fix: add `max_allowed_ahead_sec` -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/fb1fee1e85df15c9cb3be78036f4aadeb1cf1307)    
       
*1.6.3*
---
    
- 2019-01-29 (Laisky) fix: fluentd producer tags should not append env -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a2449487f3fe32b6f039815e93a7b079312cafc8)    
       
*1.6.2*
---
    
- 2019-01-25 (Laisky) docs: update CHANGELOG -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a893de9f17b909861041a41595ab0ed134ca4671)
- 2019-01-25 (Laisky) perf: upgrade zap -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/78b7ab818bb4ac2aa43a73080ceec09ff06b9fc0)
- 2019-01-25 (Laisky) build: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/ceb4068fd7da5d3e184efa8bdff922b385fccfbc)
- 2019-01-25 (Laisky) perf: use uniform clock in go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/34506fdf134a13cabb10a83743b485d30b5af95d)
- 2019-01-24 (Laisky) fix(paas-284): reduce debug log to reduce cpu -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/8462e49400488a5ab018981db5db9c692ba85077)
- 2019-01-24 (Laisky) fix: remove useless pprof entrypoint -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/5b5dc4e1764c22dab1601378af8a6639c2e6ce04)
- 2019-01-24 (Laisky) fix: check timer's ts -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/679f294dfa9e75e904c22f7a7560e8fd3551a1b2)
- 2019-01-24 (Laisky) build: upgrade iris -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1039e8fca133bf3a1df5bfa5d49e69068240d779)
- 2019-01-24 (Laisky) perf: reduce invoke time.Now -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e57ab328e1f2b70cabc8c11db251fc67fd6168eb)
- 2019-01-24 (Laisky) build: upgrade golang v1.11.5 -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/74e587f45f0d4a32a702844319d6a7dd964eddb8)
- 2019-01-24 (Laisky) docs: update docs -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d7d2a2f0dbdee9450b2d99f0b5c362493a2da337)
- 2019-01-23 (Laisky) fix(paas-282): some bugs during testing -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b45f15cbb3aa85f17f65d3067a1ba32a577c05a5)
- 2019-01-23 (Laisky) fix: fluentd sender missing env -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e4343803b78f1f69efa05e9a00eb19b0e787117d)
- 2019-01-17 (Laisky) docs: update docs -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/e52fde7d691399ae441a7e53f6c2ef43ed99131f)    
       
*1.6.1*
---
    
- 2019-01-17 (Laisky) fix: `append_time_zone` -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a08c02b84236a94f0dde389847ea037334040e8f)
- 2019-01-17 (Laisky) fix: kafka recv error -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/a38f891b9f21d0b039374fb471652f876aee69c8)
- 2019-01-17 (Laisky) docs: add docker readme -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b9b3b35e801de753b4f1f3ee7ca6cf73f17a15f2)
- 2019-01-17 (Laisky) docs: add pipeline label -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/9ab17c58a7a97d9bbce1d18dd8de65997baa4a0f)
- 2019-01-17 (Laisky) ci: `IMAGE_NAME` in gitlab-ci -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/0ca13687300928292e373d74a73a2370547ba8ed)
- 2019-01-17 (Laisky) ci: fix image bug -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/98d5f04b18ac30a398cb6c2f2d952b1ea57a6dab)
- 2019-01-16 (Laisky) ci: fix image bug -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/7fba13054185e0405e9357e6d2d87ae8e6f8b28f)
- 2019-01-16 (Laisky) ci: add proxy -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/8c641fa165592822afae709bf8a31702413e3f90)
- 2019-01-16 (Laisky) ci: add golang-stretch -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/cd29265eee96c003a37278e639113d9461c5db25)
- 2019-01-16 (Laisky) ci: enable marathon -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/622c2c2e9e6dd6820f97ec5f6238d844805dd8ec)
- 2019-01-16 (Laisky) ci: add strecth-mfs -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/943c9437f65d311062fc5242a8fa4234e93ce910)
- 2019-01-16 (Laisky) ci: fix ci yml -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b4b31312848bb6ab4a3cdca990c4dc3fdd1d9766)
- 2019-01-16 (Laisky) fix(paas-281): add gitlab-ci -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/97fda5a84ff1a6ee151a8f99ddb1e68bfc3cf311)
- 2019-01-15 (Laisky) docs: update TOC -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/768915e53ebe9aeb10e78593371f68db9b0a6b7e)
- 2019-01-15 (Laisky) docs: add cn doc -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/41298c224907b25d1bca028e9512dc5daa93abd1)
- 2019-01-15 (Laisky) style: rename dockerfile -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1a628aa5b21c324127cd67746663c0ac41a4f7f9)
- 2019-01-15 (Laisky) feat(paas-276): able to load configuration from config-server -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1bfb162f208fb927ad732d44a628e0f0a1368e75)
- 2019-01-14 (Laisky) feat(paas-275): refactory controllor - parse settings more flexible -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b232c92187d2af02331a5841b4487608cf5e5658)    
       
*1.6*
---
    
- 2019-01-11 (Laisky) build: upgrade go-utils -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/bd0d41ce183cd31aa20b6c0f8be9b6f7c4324395)
- 2019-01-10 (Laisky) fix: flatten delimiter -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/b5a10a9fcbba6988806c5dfc99ade1a040aeb78c)
- 2019-01-10 (Laisky) feat: flatten httprecv json -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/2f66b203d943c5130b9a4c6bc8c19d4b12196e70)
- 2019-01-10 (Laisky) fix: no not panic when got incorrect fluentd msg -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/269b6709fa888cc1a3b8ec3072e778017dfc8659)
- 2019-01-10 (Laisky) fix(paas-272): do not retry all es msgs when part of mesaages got error -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/6b9ac20d694c7575de9998bf1cf5ad173f300c3a)
- 2019-01-10 (Laisky) fix: add more err log -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/9e3d45b0e36e50de1711a2f77d0ea3a7e15e69f9)
- 2019-01-10 (Laisky) style: less hardcoding -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/0c44b3b6e033fadad7041ef0693d04a85fd1f82c)
- 2019-01-10 (Laisky) feat(paas-270): add signature in httprecv -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/d849ebf395dcd97df26c8cded3c2d767a34797ef)
- 2019-01-09 (Laisky) feat(paas-259): support external log api -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1d2b8555118d82757e288dbeeca2c581777bfe14)
- 2019-01-09 (Laisky) build: upgrade glide & marathon -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/f6b7d3463cfc8db9cc4925fb762f6a0ea66fc903)
- 2019-01-07 (Laisky) feat(paas-261): add http json recv -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/872be3b69477703a74b18f25c87361b95fe39d74)
- 2019-01-09 (Laisky) feat(paas-265): use stretch to mount mfs -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/af9230c7c027dbef03c452c5cbc89197b764f149)    
       
*1.5.4*
---
    
- 2019-01-07 (Laisky) fix(paas-258): backup logs missing time -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/1826a82330e485421568439ea7e24c30327a3b0e)
- 2019-01-02 (Laisky) docs: update changelog -> [view commit](http://gitlab.pateo.com.cn:10080/PaaS/go-fluentd/commit/db5cb7df76e38fb5b75284a53eb05f2fd678f3c1)    
       
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
