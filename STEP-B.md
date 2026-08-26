# 段B〜段E — 箱を kamal から haloy へ渡す

段A（haloyd を横に建てる）は済んでいる。ここからは**本番が動く**。

## なぜ一続きなのか

入口は 80/443 の一組しかなく、それを持てるのは kamal-proxy か haloy-proxy の
どちらか一方だけ。だから「natadeco だけ先に移す」ができない ── 移した瞬間、
外から来た 443 は kamal-proxy に着き、その行き先が消えている。

なので **箱の五つ全部を haloy に載せてから、入口を一度に渡す**。
段B〜段D で載せる（外からはまだ見えない）、段E で渡す。

止まる時間は「載せた店子が、入口を渡すまでの間」。段B〜段E を続けて
やれば、natadeco と sukhi はそれぞれ数分。watch-mjw / techo /
transit-f3liz は段Dまで無傷のまま。

**逆順のほうが止まりが短い**なら、先に段D（小さい三つ）を済ませてから
natadeco → sukhi に行ってもいい。下は「小さいものから」で並べてある。

## 前提

- 段A が済んでいる（haloyd/haloy-proxy が 8081/8444 で active）
- ssh トンネルが張られていて、`haloy status` が答える
- **証明書を置く**（段E の直前でよいが、忘れると入口を渡した瞬間に
  自己署名が出る）:

```sh
# combined PEM(鍵 + 証明書)を <domain>.pem という名前で。
# 順番は key が先でも cert が先でもよい(tls.X509KeyPair が両方拾う)。
for pair in "natadeco.com:$HOME/repos/natadeco-deploy/config/tls" \
            "sukhi.f3liz.casa:$HOME/repos/sukhi-deploy/config/tls"; do
  d=${pair%%:*}; dir=${pair#*:}
  cat "$dir/origin.key" "$dir/origin.crt" > /tmp/$d.pem
  scp /tmp/$d.pem rocky@$DEPLOY_HOST:/tmp/
  ssh rocky@$DEPLOY_HOST "sudo install -o haloy -g haloy -m 0600 /tmp/$d.pem /var/lib/haloy/certificates/$d.pem && rm -f /tmp/$d.pem"
  rm -f /tmp/$d.pem
done

# haloyd の設定に「これは自分のものではない」と書く
ssh rocky@$DEPLOY_HOST 'sudo tee -a /etc/haloy/haloyd.yaml >/dev/null <<YAML

certificates:
  external:
    - natadeco.com
    - sukhi.f3liz.casa
YAML'
ssh rocky@$DEPLOY_HOST 'sudo systemctl restart haloyd'
```

`/var/lib/haloy/certificates` の実際の名前は
`ls /var/lib/haloy` で確かめてから（`constants.CertStorageDir`）。

## 段B — natadeco

target 名（`natadeco-postgres` / `natadeco-nats`）が kamal のコンテナ名と
同じなので、**先に kamal 側を止める**。ボリュームは host の絶対パスなので
データはそのまま。

```sh
cd ~/repos/natadeco-deploy

# 1. いまの姿を控えておく（戻すときの照合用）
ssh rocky@$DEPLOY_HOST 'docker ps -a --format "{{.Names}}|{{.Status}}" | grep natadeco'

# 2. kamal を止める（ここから natadeco.com が落ちる）
DEPLOY_HOST=$DEPLOY_HOST kamal app stop
ssh rocky@$DEPLOY_HOST 'docker stop natadeco-postgres natadeco-nats && docker rm natadeco-postgres natadeco-nats'

# 3. haloy で建てる。postgres は protected なので名指しが要る
#    （secret は環境から読まれる。.kamal/secrets はシェルで source できない
#      ので、quote 付きの .env を用意しておくこと）
~/repos/haloy/dist/haloy deploy natadeco-postgres natadeco-nats
~/repos/haloy/dist/haloy deploy natadeco

# 4. 確かめる（まだ外からは見えない。haloy-proxy 越しに)
ssh rocky@$DEPLOY_HOST 'curl -s -o /dev/null -w "%{http_code}\n" -H "Host: natadeco.com" http://127.0.0.1:8081/api/v1/deco'
~/repos/haloy/dist/haloy status --all
ssh rocky@$DEPLOY_HOST 'docker logs --tail 30 $(docker ps -qf name=natadeco)' # migration が一度走った跡
```

戻すなら: haloy 側を止めて（`haloy stop natadeco` 他）、
`kamal deploy --skip-push --version=v0` と `kamal accessory boot postgres nats`。
同じ host のパスを見るのでデータは戻る。

## 段C — sukhi

同じ形。image は `sukhi-fedi-combined:v0` として箱に焼いてある。

```sh
cd ~/repos/sukhi-deploy
DEPLOY_HOST=$DEPLOY_HOST kamal app stop
ssh rocky@$DEPLOY_HOST 'docker stop sukhi-fedi-deployex sukhi-fedi-postgres sukhi-fedi-nats sukhi-fedi-rustfs &&
                        docker rm  sukhi-fedi-deployex sukhi-fedi-postgres sukhi-fedi-nats sukhi-fedi-rustfs'

~/repos/haloy/dist/haloy deploy sukhi-fedi-postgres sukhi-fedi-nats sukhi-fedi-rustfs
~/repos/haloy/dist/haloy deploy sukhi-fedi-app
~/repos/haloy/dist/haloy deploy sukhi-fedi-anubis

ssh rocky@$DEPLOY_HOST 'curl -s -o /dev/null -w "%{http_code}\n" -H "Host: sukhi.f3liz.casa" http://127.0.0.1:8081/nodeinfo/2.1'
```

**DeployEx はここで役目を終える。** `/var/lib/sukhi-fedi/{releases,deployex}`
と `~/sukhi-fedi-deployex/` は、しばらく残しておく（戻り道）。

## 段D — 残り三つ

haloy.yaml をまだ書いていない。中身は kamal の deploy.yml を写すだけで、
段B/段C と同じ形になる。

- `transit-f3liz` — web 一つ
- `techo` — web 一つ
- `watch-mjw` — web + postgres + nats

## 段E — 入口を渡す

ここまでで五つ全部が haloy に載っていて、haloy-proxy が 8081/8444 で
正しく配れていること。証明書も置いてあること。

```sh
# 1. kamal-proxy を降ろす
ssh rocky@$DEPLOY_HOST 'docker stop kamal-proxy'

# 2. haloy-proxy を 80/443 へ（drop-in を消すと既定に戻る）
ssh rocky@$DEPLOY_HOST 'sudo rm -f /etc/systemd/system/haloy-proxy.service.d/ports.conf &&
  sudo systemctl daemon-reload && sudo systemctl restart haloy-proxy'

# 3. 外から
for h in natadeco.com sukhi.f3liz.casa transit.f3liz.casa; do
  curl -s -o /dev/null -w "  $h %{http_code}\n" https://$h/
done
```

戻り道は drop-in を書き戻して `docker start kamal-proxy`。

そのあと、両 repo の `haloy.yaml` の `server:` を箱のアドレスへ戻し
（ssh トンネルが要らなくなる）、haloyd の API ドメインを決める。

## 片づけ（固まってから）

- `bin/release-on-box.sh` / `make release` / `push-deployex-config`
- `infra/deployex/`
- 箱の `/var/lib/sukhi-fedi/{releases,deployex}` / `~/sukhi-fedi-deployex/`
- kamal 一式（`config/deploy.yml` は消さずに残す判断もある）
