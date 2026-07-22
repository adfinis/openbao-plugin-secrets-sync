# Changelog

## 0.1.0-preview.1 (2026-07-22)


### Features

* **api:** add actionable sync diagnostics ([#27](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/27)) ([fc1ff28](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/fc1ff28d5691eab4689bbcd34497867512f5f21d))
* **api:** add source write shorthand ([#29](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/29)) ([da40237](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/da40237865796cd138d3815932c715b507b60174))
* **api:** move association defaults to info ([#26](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/26)) ([dd40ccc](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/dd40ccc8bbf88297bccca3de61abb93ea2201b81))
* **api:** support paginated list endpoints ([#11](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/11)) ([1bcba00](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/1bcba00d78d451d66ed0e83b1e74c06dfc363a84))
* **association:** guard identity-changing updates ([#25](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/25)) ([b0af1a4](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/b0af1a4c18651764ed41703cb61cab1341841125))
* **association:** use destination refs for API addressing ([#24](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/24)) ([52d0968](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/52d09681b057643b112d82f5a4c7a7f3b2b8d86d))
* **aws:** add web identity auth ([#31](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/31)) ([3322b08](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/3322b082dcd1d735315beb3e8d4edfe1ee2ff5da))
* **backend:** add fake destinations and associations ([af770e6](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/af770e68ca28b5294c1a5fd2458ad6914ee3b444))
* **backend:** add lifecycle and delete semantics ([e2e4cab](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/e2e4cab74be6a6f049f027919a6b088aee2ac04f))
* **backend:** add local versioned writes ([ef69149](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/ef691494b2c0eef527eab56757c02ed2c4eb08d1))
* **backend:** add metadata read list and delete ([8cc2fd1](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/8cc2fd1c4a223de08333fcfceeabf72c4f2dc933))
* **backend:** add provider diagnostics and planning ([638c126](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/638c126c9b2f9e0f608e5e036d7ec2e8b47c51ba))
* **backend:** add queue operation controls ([6253b6a](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/6253b6a6f2652f2604e2603c516453ca324074ad))
* **backend:** add source policy and provider dispatch ([3e00a7d](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/3e00a7d79024547c486e5ee2d10a7137680457e7))
* **backend:** add undelete and destroy endpoints ([ef7b586](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/ef7b5862cdf01a6d9e5d834b7145e60e70d97ba1))
* **backend:** improve sync response UX ([6835aa0](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/6835aa0c733726b08ea6ce667075a56dc3ac2fa8))
* **backend:** process fake outbox operations ([12bb4b4](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/12bb4b488f5291135d8d384de0f4ee1cf371fc70))
* **backend:** recover incomplete enqueue intents ([7658781](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/76587813121846f83767628178745beb838bf77e))
* **config:** relax runtime sync gates ([#19](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/19)) ([4b1acc7](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/4b1acc79abcef02a7fa3d062fc10f94c1bc66e9f))
* **drift:** add background drift repair ([#21](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/21)) ([ffd0780](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/ffd078085e18a778cad95b70db0d21795fe25a79))
* **gitlab:** add project variable provider ([54055f4](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/54055f4f7b9a3d099d5ca6d8d48ace8dcfcf43f7))
* **gitlab:** move variable options to associations ([#58](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/58)) ([bbfd739](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/bbfd739bce0ed7e0c58e8a4a3186be157fa422f7))
* **gitlab:** use readable variable metadata descriptions ([#23](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/23)) ([a783fae](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/a783fae5becbc621f6cfdfdf962babf9daf767f5))
* **hardening:** bind provider ownership to runtime identity ([d769ad1](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/d769ad1db9bcce66292199b4e9e2bb1a2b2eba18))
* **hardening:** persist runtime identity records ([db743bc](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/db743bc3da39502542a7ba83ae36cb1951380620))
* **kubernetes:** add token auth and data mapping ([#17](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/17)) ([3823c6e](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/3823c6ec00ac6c9756ce7be90e31bfd5d197ddbe))
* **observability:** add OpenTelemetry metrics surface ([5dc9914](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/5dc991479c34fc8f72704bc895e07f02f03f7143))
* **observability:** add operational telemetry ([6172c70](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/6172c7009f7e779308f50975f3666359a00b074b))
* **providers:** add aws destination auth config ([7386980](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/738698008f61e65ef834ff962ef316e2db085b23))
* **providers:** add Kubernetes Secrets provider ([dbf6143](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/dbf6143508bf7d5a8ee256028a4b1350de47c7f0))
* **providers:** harden aws destination config ([f9b549e](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/f9b549e3d33a7680bba4f93a99beef0553b052df))
* **providers:** implement aws secrets manager client ([ef7c461](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/ef7c461a6e179f90aaf9a236d5352b77da8f6849))
* **queue:** add deterministic drain and localstack e2e ([c3ba07a](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/c3ba07ae50d430166983390e97aaf896e2fdca1a))
* **queue:** add event-triggered dispatch ([#22](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/22)) ([8de43ae](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/8de43ae67dc2f7e697605f9aa34f786b5bce1e85))
* **queue:** harden dispatcher ownership ([e8310af](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/e8310af2f5f72e714a0d699dbc7becf7c0c8304f))
* **reconcile:** add provider read-state workflow ([038f9f9](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/038f9f9b88b9ec74f03f5bbfe6ad668fd0911451))
* **security:** add delegated mode guardrails ([96c6491](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/96c64912731b6c845e8885f99dd372220eae5438))
* **security:** add hardened source sync controls ([#50](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/50)) ([e9b076b](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/e9b076b74eb2bd73e177672309dcdf435daed641))
* **security:** constrain destination policy ([242bf5e](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/242bf5e5f5d4954c0ef52c9274d4ca6fbae20f33))
* **storage:** transact source mutations ([#75](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/75)) ([47c1450](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/47c1450dd25089e95ba5e42c13b03b0063211009))
* **sync:** add secret-key granularity ([bc7b81e](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/bc7b81eb2c5a6b1a1bfd3cc9e16831e045edcab8))
* **sync:** stabilize remote mutation safety ([958bb46](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/958bb467e085f0ae3020a33f1abc840cd38a2097))
* **ux:** add readiness checks and API spec ([ba6bcbd](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/ba6bcbdd2c88c645e2802b33c437e577051a1602))
* **ux:** simplify source onboarding ([ebd80f5](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/ebd80f563809718614026cb74a7e188ce40d0673))


### Bug Fixes

* **api:** harden API freeze contract ([#43](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/43)) ([81cdbce](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/81cdbce7d076563b3c3589443119781e2ac3b64b))
* **api:** polish CLI-visible edge cases ([#44](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/44)) ([dc376f7](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/dc376f76132b578193564ff599b5b1c188978387))
* **aws:** harden provider contract ([#60](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/60)) ([e3f8944](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/e3f89448f91b76b1852fa67757273d04b437cd2e))
* **aws:** make web identity token readable in e2e ([#32](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/32)) ([3094fca](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/3094fcace8c8eeb4f2309af80c51b679b930ff22))
* **aws:** recover owned secrets scheduled for deletion ([#15](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/15)) ([3f487ad](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/3f487ad174eeabf4e4f26b62d8bab8f0f00de2ba))
* **backend:** enforce create and update ACLs ([#71](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/71)) ([650166e](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/650166e74ac17b3b1ec07fd2b7f680ba99fae233))
* **backend:** harden backend safety invariants ([#30](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/30)) ([8c46bc2](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/8c46bc22e2613ed93274c079777d07f84f944794))
* **backend:** initialize through plugin lifecycle ([#67](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/67)) ([bfe4ffc](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/bfe4ffcaad3b63517cf24a6bae218a0f0e76d90b))
* **backend:** stabilize sync queue and lifecycle consistency ([#10](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/10)) ([4f8f60c](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/4f8f60c6be13aab4f639dc157bdb86e95754f4db))
* **gitlab:** validate variable attributes and masked payloads ([#14](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/14)) ([425c3ee](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/425c3ee6a29a781e32964dddb787b861c0d1ae8b))
* **k8s:** harden convergence and deletion safety ([#59](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/59)) ([56593de](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/56593de28984cbf9415b52158d1cb48a967b16ef))
* **kubernetes:** use OpenBao metadata namespace ([#61](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/61)) ([a022cda](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/a022cda6705042aa80c8058f104fe007fbe80871))
* **provider:** cache destination runtimes ([#16](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/16)) ([0d550c6](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/0d550c69ef840745ae409d8222cc1735e4e2e298))
* **provider:** classify transport errors as unavailable ([#13](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/13)) ([75a8385](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/75a8385e5b28a6ba5ae48520b7470449fb37f36b))
* **provider:** detect remote value drift ([#20](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/20)) ([860185d](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/860185dfa0fdb278e785b54bf68d9e0390b3a7de))
* **provider:** enforce network bounds at dial time ([#57](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/57)) ([fb770c7](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/fb770c73a89da7a8c28d961b2a81dded271405d2))
* **provider:** honor disabled runtime caching ([#68](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/68)) ([5d30d8e](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/5d30d8e49eae968ed347677425ba1885981c222a))
* **queue:** forward drains from HA standbys ([#72](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/72)) ([4bde590](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/4bde5906da63a75830455f08edd2979466f8a8b5))
* **queue:** harden crash-safe dispatch ([#35](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/35)) ([27a9569](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/27a9569e47dc5b492a012c77ab8df3c32e57d5e9))
* **queue:** harden outbox index consistency ([#51](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/51)) ([51bff95](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/51bff95a843c1e5efa71e1b9ff9ffbbac030d7a5))
* **queue:** honor active claims and bound terminal retention ([#56](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/56)) ([7a49ae6](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/7a49ae6037461f24f70e3bd759117984298c47ff))
* **security:** guard provider endpoints against SSRF ([583e735](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/583e735f5c6a032c10bb27508c74731ebc9b3e39))
* **security:** update vulnerable Go dependencies ([#76](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/76)) ([2779f3a](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/2779f3a1e96e1456ca7f5bfa6cc3af3ea8984819))
* **source:** preserve enqueue intent through metadata commit ([#54](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/54)) ([af662a5](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/af662a530ff08ae06eebb5151fd886dd27fd7559))
* **storage:** derive destination sensitive keys by provider ([#34](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/34)) ([c33d99b](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/c33d99b1816d6c60e82d8cf237841de21277278c))
* **storage:** preserve coherent configuration updates ([#55](https://github.com/adfinis/openbao-plugin-secrets-sync/issues/55)) ([ac47324](https://github.com/adfinis/openbao-plugin-secrets-sync/commit/ac473242ec99e006c1a56b8ec2f56521dcccceaf))

## Changelog

All notable changes to this project will be documented here.

## Unreleased

### Changed

- Move static association defaults from association, plan, and lifecycle
  responses to the new `info` endpoint.
