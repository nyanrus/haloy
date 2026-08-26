# 箱 $DEPLOY_HOST を kamal から haloy へ

このフォーク（branch `nyanrus`）で、sukhi.f3liz.casa と natadeco.com を
haloy に移すための手順と、まだ空いている穴。2026-08-26 に書いた。

**kamal と DeployEx の両方を畳む。** 設定の正本は各 repo の `haloy.yaml`:
- `~/repos/sukhi-deploy/haloy.yaml` — anubis / app / postgres / nats / rustfs
- `~/repos/natadeco-deploy/haloy.yaml` — natadeco / postgres / nats

## DeployEx を畳んで、何が変わるか

アプリの版が tarball から **image** に戻る。焼くのは今までどおり箱
（`IMAGE_PREFIX=<prefix> bin/build-on-box.sh combined`）で、haloy は
`build: false` / `pull_policy: never` でそれを使うだけ。

**保たれるもの**

- *migration* ── `combined/rel/entrypoint.sh` が
  `combined eval 'SukhiFedi.Release.migrate_all()'` を走らせてから
  `start` に exec する。image が自分でやる。
  （haloy の `pre_deploy` / `post_deploy` は `cmdexec.RunCommand` で
  **手元のマシン**の設定ディレクトリで走るので、コンテナの中で何かを
  走らせる段は無い。噛み合っているのは偶然ではなく、image で配る形が
  もともとそうなっているから）
- *無停止* ── haloy の rolling は新しいコンテナが健康になって proxy が
  切り替わってから古いのを止める（`haloyd/updater.go:221` が現
  deploymentID **以外**を止める）。sukhi は anubis が間に居るが、
  anubis はコンテナ名ではなく **hostname**（`sukhi-fedi-app`）で引くので、
  入れ替わりの数秒はどちらに当たっても健康な版になる。

**失うもの**

- DeployEx のダッシュボード ── remote IEx、observer、ホストの tmux
  シェル。代わりは `haloy status` / `haloy logs` / `haloy exec`
- **一行直して 15 秒**の差し替え。ただし思っていたほど高くはなかった ──
  2026-08-26 に `IMAGE_PREFIX=sukhi-fedi bin/build-on-box.sh combined` を
  実測（HEAD `7c89cf2`、deps は cache に乗ったまま、アプリのコードは全部
  焼き直し）:

  | 段 | 秒 |
  |---|---|
  | deps（`mix deps.compile`） | 0（CACHED） |
  | ソースの COPY | ~5 |
  | `mix release`（61+27+242 ファイルの compile） | **31.6** |
  | image の export | **12.1** |
  | push | ほぼ layer 使い回し（natadeco-combined から mount） |
  | 合計 | **~50 秒** |

  15 秒 → 50 秒。分の単位ではなかった。しかもこの 31.6 秒は「242 ファイル
  全部」なので、一行直したときも同じだけかかる（Dockerfile はアプリの
  コードを一枚の COPY で入れるので、増分 compile が効かない）。
  そこを刻めば縮む余地はある ── いまは触らない。

**畳んだあと要らなくなるもの**（移行が固まってから消す。いまは残す）

- `bin/release-on-box.sh` と `make release` / `release-images`(deployex 側) /
  `push-deployex-config`
- `infra/deployex/`（Dockerfile / entrypoint.sh / deployex.yaml /
  deployex.natadeco.yaml）
- 箱の `/var/lib/sukhi-fedi/releases` / `/var/lib/sukhi-fedi/deployex` /
  `~/sukhi-fedi-deployex/`

どちらも `haloy validate-config` と `haloy targets` を通してある。

## この箱に足りなかったもの（フォークで足した）

| 足したもの | branch | 何のため |
|---|---|---|
| `command` / `resources` / `healthcheck` / `hostname` / `publish` | `feat/container-runtime-options` | postgres の `-c shared_buffers=...`、二コア箱のメモリ予算、`pg_isready`、BEAM のノード名、5001/5432/4222 の loopback |
| `HALOY_PROXY_HTTP_ADDR` / `_HTTPS_ADDR` | `feat/proxy-listen-addresses` | **本番を止めずに横で建てて確かめるため** |

## まだ空いている穴

1. **証明書。** haloy は ACME(HTTP-01) しか持たない。いまは Cloudflare の
   origin 証明書（SAN に `*.natadeco.com` / `*.f3liz.casa`、2041年まで）を
   kamal-proxy が持っている。haloy に置いても `hasConfigurationChanged` が
   必ず「変わった」と判定して ACME 証明書に差し替える ── ワイルドカードは
   `IsValidDomain` が弾くので設定側で一致させる逃げ道も無い。
   **道は二つ**: (a) external cert のパッチを書く（当たり所 4 箇所・
   50〜100 行、配る側は 0 行）、(b) ACME を受け入れる（Cloudflare は
   HTTP-01 を通せる。origin 証明書は捨てることになる）。
2. **箱の入口は一組しかない。** 80/443 はいま kamal-proxy が握っていて、
   その後ろに **五つ**居る ── sukhi-fedi / natadeco / watch-mjw / techo /
   transit-f3liz。haloy-proxy に渡す瞬間、haloy が知らない三つは 404 に
   なる。**この二つだけ移して終わり、にはならない。**
3. **secret の渡しかた。** haloy の `from: {env: X}` は `haloy` を叩く
   シェルの環境から取る。`.kamal/secrets` はシェルでは source できない
   （10 行目の値に `)` が入っていて parse error になる、実測）。
   `.env` を quote 付きで作り直すか、haloy の `secret_providers`
   （SOPS / 1Password）へ移すか。
4. **一回きりの仕事の型が無い。** `nats-bootstrap` は stream を作る
   使い捨て。もう在るので普段は要らない。作り直すときだけ手で
   `docker run --rm --network haloy ...`（各 haloy.yaml の頭に書いてある）。

## 段取り（案）

```
段A  箱に haloyd / haloy-proxy を入れる（proxy は別ポートで）
     → 手元で arm64 に焼く:
         GOOS=linux GOARCH=arm64 go build -ldflags "-s -w \
           -X github.com/haloydev/haloy/internal/constants.Version=nyanrus-<sha>" \
           -o out/<name> ./cmd/<name>      # haloy / haloyd / haloy-proxy
       上流の install-haloyd.sh は GitHub releases から落とす作りなので、
       焼いたものを /usr/local/bin へ置いてから unit だけ作らせる。
     → systemd drop-in でポートをずらす:
         /etc/systemd/system/haloy-proxy.service.d/ports.conf
         [Service]
         Environment=HALOY_PROXY_HTTP_ADDR=:8081
         Environment=HALOY_PROXY_HTTPS_ADDR=:8444
       ※ 8080 は haloyd の ACME チャレンジサーバが持っているので使えない
     → verify: haloyd が上がる。80/443 は kamal-proxy のまま、本番は無傷

段B  natadeco を haloy で建てる（kamal 版と並走）
     → 名前がぶつかる: kamal の natadeco-postgres と同名になる。
       **先に kamal 側を止める**（`kamal accessory remove postgres nats` +
       `kamal app stop`）。ボリュームは host のパスなのでデータは残る。
     → verify: curl --resolve natadeco.com:8444:127.0.0.1 でも、
       素の http://127.0.0.1:8081 に Host: natadeco.com でもいい
     → 戻り道: haloy 側を止めて kamal deploy に戻す（同じ host パスを見る）

段C  sukhi を同じやりかたで
     → combined の image は **2026-08-26 に焼いて箱にある**
       (`127.0.0.1:5000/sukhi-fedi-combined:v0`、289MB、
        SHA タグ `7c89cf2505ef...` も同じ digest)。焼き直すときは
         cd ~/repos/sukhi-fedi
         IMAGE_PREFIX=sukhi-fedi bin/build-on-box.sh combined
     ※ build-on-box.sh は `:v0` と `:<full-sha>` の二つを打つ。haloy の
       `image.tag` を SHA のほうにすれば本物の rollback ができる
       （`haloy rollback` / `rollback-targets`）── いまは `:v0` のまま
     → kamal 側を止める（anubis + deployex + postgres/nats/rustfs）。
       ボリュームは host のパスなのでデータは残る
     → verify: 同上。加えて anubis 越し（PoW の関所が新しい app を
       引けているか）と、`docker logs` に migration が一度走った跡

段D  残り三つ（watch-mjw / techo / transit-f3liz）を haloy へ
     → ここを飛ばすと段E で三つが落ちる

段E  入口を入れ替える
     → 証明書の穴（上の 1）をどちらかの道で先に塞ぐ
     → kamal-proxy を止め、drop-in を消して haloy-proxy を 80/443 へ
     → 戻り道: drop-in を戻して kamal proxy boot
```

段B と段C が「一旦 sukhi-fedi と natadeco の kamal 代替」の中身で、
そこまでは **80/443 に触らない**ので本番の入口は無傷のまま進められる。
