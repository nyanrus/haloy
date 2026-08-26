# 段A — 箱に haloyd と haloy-proxy を入れる

本番の入口(80/443)には触らない。haloy-proxy は **loopback の別ポート**で
建てるので、kamal-proxy はそのまま五つの店子を配り続ける。
失敗しても止まるものは無く、戻り道は「二つの service を消す」だけ。

前提: 手元で `dist/` を焼いてあること（`haloy` / `haloyd-linux-arm64` /
`haloy-proxy-linux-arm64`）。上流の install スクリプトは GitHub releases
から落とす作りで、このフォークには release が無いので、そこだけ手で置く。

```sh
export DEPLOY_HOST=<箱の IP>   # 公開リポジトリなので伏せてある
cd ~/repos/haloy
```

## A-1. バイナリを箱へ

```sh
scp dist/haloyd-linux-arm64 dist/haloy-proxy-linux-arm64 rocky@$DEPLOY_HOST:/tmp/
ssh rocky@$DEPLOY_HOST 'sudo install -m 0755 /tmp/haloyd-linux-arm64 /usr/local/bin/haloyd &&
  sudo install -m 0755 /tmp/haloy-proxy-linux-arm64 /usr/local/bin/haloy-proxy &&
  rm -f /tmp/haloyd-linux-arm64 /tmp/haloy-proxy-linux-arm64 &&
  /usr/local/bin/haloyd version && /usr/local/bin/haloy-proxy version'
```

## A-2. ユーザと初期化

上流スクリプトの step 3 / 5 / 6 に当たるところ。docker group に入れるのは
haloyd が docker を触るため。

**`haloyd` は絶対パスで呼ぶ。** Rocky の sudo は `secure_path` が
`/sbin:/bin:/usr/sbin:/usr/bin` で、`/usr/local/bin` が入っていない
（2026-08-26 に `command not found` で踏んだ）。上流のスクリプトが
`"$INSTALL_PATH" init` と書いているのは、そのため。

```sh
ssh rocky@$DEPLOY_HOST 'sudo sh -c "
  id haloy >/dev/null 2>&1 || useradd --system --shell /sbin/nologin --home-dir /var/lib/haloy --no-create-home haloy
  usermod -aG docker haloy
  /usr/local/bin/haloyd init
  chown -R haloy:haloy /var/lib/haloy /etc/haloy
  chmod 700 /var/lib/haloy /etc/haloy
"'
```

## A-3. systemd の unit（上流スクリプトのものそのまま）

```sh
ssh rocky@$DEPLOY_HOST 'sudo tee /etc/systemd/system/haloy-proxy.service >/dev/null <<UNIT
[Unit]
Description=Haloy Proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=haloy
Group=haloy
ExecStart=/usr/local/bin/haloy-proxy serve
Restart=always
RestartSec=5
Environment=HALOY_DATA_DIR=/var/lib/haloy
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/haloy
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT'

ssh rocky@$DEPLOY_HOST 'sudo tee /etc/systemd/system/haloyd.service >/dev/null <<UNIT
[Unit]
Description=Haloy Daemon
After=network-online.target docker.service haloy-proxy.service
Requires=docker.service
Wants=network-online.target haloy-proxy.service

[Service]
Type=simple
User=haloy
Group=haloy
ExecStart=/usr/local/bin/haloyd serve
Restart=always
RestartSec=5
Environment=HALOY_DATA_DIR=/var/lib/haloy
Environment=HALOY_CONFIG_DIR=/etc/haloy
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/haloy
ReadOnlyPaths=/etc/haloy
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT'
```

## A-4. ポートをずらす（この段のいちばん大事なところ）

これが無いと haloy-proxy が 80/443 を取りに行って、kamal-proxy と衝突する。
loopback に縛るので、外からは見えない。8080 は haloyd の ACME チャレンジ
サーバが持っているので避けている。

```sh
ssh rocky@$DEPLOY_HOST 'sudo mkdir -p /etc/systemd/system/haloy-proxy.service.d &&
  sudo tee /etc/systemd/system/haloy-proxy.service.d/ports.conf >/dev/null <<CONF
[Service]
Environment=HALOY_PROXY_HTTP_ADDR=127.0.0.1:8081
Environment=HALOY_PROXY_HTTPS_ADDR=127.0.0.1:8444
CONF'
```

## A-5. 起動

```sh
ssh rocky@$DEPLOY_HOST 'sudo systemctl daemon-reload &&
  sudo systemctl enable --now haloy-proxy haloyd &&
  sleep 3 && systemctl is-active haloy-proxy haloyd'
```

## A-6. 確かめる

```sh
# 1. 本番が無傷か（いちばん大事）
curl -s -o /dev/null -w "natadeco %{http_code}\n" https://natadeco.com/api/v1/deco
curl -s -o /dev/null -w "sukhi    %{http_code}\n" https://sukhi.f3liz.casa/nodeinfo/2.1

# 2. 80/443 を握っているのが誰か（kamal-proxy のままであること）
ssh rocky@$DEPLOY_HOST 'sudo ss -ltnp | grep -E ":(80|443|8081|8444|9922)\s"'

# 3. haloy-proxy が別ポートに居て、ログに警告が出ていること
#    ("HTTP listener is not on port 80; ACME HTTP-01 challenges cannot reach this proxy")
ssh rocky@$DEPLOY_HOST 'sudo journalctl -u haloy-proxy -n 20 --no-pager'
ssh rocky@$DEPLOY_HOST 'sudo journalctl -u haloyd -n 20 --no-pager'
```

## A-7. 手元の CLI を繋ぐ

haloyd の API は loopback の 9922。proxy も TLS も通さず、ssh のトンネルで
そこへ直接繋ぐ ── `127.0.0.1:9922` は `IsLocalhost` に当たるので、CLI は
素の HTTP で話す。

```sh
# トンネル（-f で背後へ。ExitOnForwardFailure でポートが埋まっていたら
# 黙って成功しない）
ssh -f -N -o ExitOnForwardFailure=yes -L 9922:127.0.0.1:9922 rocky@$DEPLOY_HOST

# 認証なしで届くか。ここに haloyd と、どの版が動いているかが出る
curl -s http://127.0.0.1:9922/health
#=> {"status":"ok","version":"nyanrus-<sha>","service":"haloyd"}

# トークン（`config get ... --raw` が素の値。`--raw` が無いと飾りが付く）
TOKEN=$(ssh rocky@$DEPLOY_HOST 'sudo /usr/local/bin/haloyd config get api-token --raw')

# 認証つきで通るか
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:9922/v1/version
#=> haloyd と haloy-proxy の版、"haloy_proxy_compatible":true

# CLI に登録
./dist/haloy server add 127.0.0.1:9922 "$TOKEN"
```

畳むときは `pkill -f 'L 9922:127.0.0.1:9922'`。

**2026-08-26 に実際に通した。** `/health` が `nyanrus-9fa43f9` を返し、
`/v1/version` が haloyd と haloy-proxy 両方の版と
`haloy_proxy_compatible: true` を返し、`/v1/status/natadeco` は 404
（まだ何も載せていないので、それで正しい）。本番は両方 200 のまま、
80/443 は docker-proxy(kamal) が握ったまま。

haloy.yaml の `server:` は、この段のあいだ `127.0.0.1:9922` にしておく
（段Eで箱の名前に戻す）。

## 戻り道

```sh
ssh rocky@$DEPLOY_HOST 'sudo systemctl disable --now haloyd haloy-proxy &&
  sudo rm -f /etc/systemd/system/haloyd.service /etc/systemd/system/haloy-proxy.service &&
  sudo rm -rf /etc/systemd/system/haloy-proxy.service.d &&
  sudo systemctl daemon-reload'
```
`/var/lib/haloy` と `/etc/haloy` は残る（消すなら `sudo rm -rf` を足す）。
docker のコンテナはまだ一つも作っていないので、店子には何も起きない。
