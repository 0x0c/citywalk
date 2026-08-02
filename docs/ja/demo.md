[English](../demo.md) · **日本語**

# デモの実行方法

このドキュメントは、`cmd/demo` というプログラムの使い方を説明します。`cmd/demo` は、フェーズ1
のサーバーを動かし、一通りの操作をするプログラムです。フェーズ1のサーバーとは、定義・配信対象
解決・端末向け配信の3サービスが、1つの PostgreSQL と1つの Redis を共有して動くプロセスを指します。
詳しくは
[CW-0010](../../roadmaps/CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack-ja.md)
のUnit 11に書いてあります。`cmd/demo` は、端末を1台登録し、配信対象となる利用者の集合(セグメント)
を定義し、メッセージを作成して公開します。続けて、そのメッセージを端末へ同期・確認し、記録します。
各手順は、実際のモバイル端末や実際の施策担当者がそれぞれ単独で行うリクエストです。プログラムは
これらを、使い捨てのデータベースに対して連続実行します。本リポジトリの他のドキュメントを読んで
いなくても、このドキュメントだけで実行できます。

## 前提条件

- [Go](https://go.dev/)([`go.mod`](../../go.mod) が指定するバージョン)。
- `docker compose` プラグインを含む [Docker](https://www.docker.com/)。PostgreSQL と Redis を
  動かすために使います。PostgreSQL 16 と Redis 7 を `localhost` 上で動かせるなら、他の手段でも
  構いません。その場合は、次の節にある `-postgres-dsn` と `-redis-addr` の各フラグを、実際の
  接続先に合わせて変更してください。

## 実行手順

PostgreSQL と Redis を起動してから、プログラムを実行します。

```bash
make demo-up   # docker compose -f deploy/docker-compose.yml up -d --wait
make demo      # go run ./cmd/demo
```

`cmd/demo` は、実行するネットワーク呼び出し1つごとに、番号付きの手順とその結果を表示します。
[`migrations/`](../../migrations) 配下のマイグレーションは、プログラム自身が適用します。別途
マイグレーションを実行する必要はありません。一通りの操作が終わったあとも、プログラムは
`http://127.0.0.1:8085` でサーバーを動かし続けます。登録した端末に対してそのまま打てる `curl`
コマンドも表示するため、手元でさらに操作を試すときに使ってください。停止するときは Ctrl-C を
押し、続けてコンテナも停止します。

```bash
make demo-down  # docker compose -f deploy/docker-compose.yml down -v
```

`make demo-up` で起動したコンテナを止めないまま、`make demo` を再実行しても構いません。前回の
実行が残したテーブルの内容と Redis のキーは、プログラムが新しいデータを書き込む前に自分で消し
ます。そのため、何度実行しても同じ結果になります。

## 各手順が示すもの

- **端末の登録**：`ChannelService.Register` を、属性 `country: "JP"` だけを添えて呼び出します。
  応答には端末識別子とアクセストークンが含まれ、以後の呼び出しはすべて、このアクセストークンを
  ベアラートークンとして提示します。モバイル端末が初めて起動したときの動作に相当します。
- **配信対象セグメントの定義**：`country == "JP"` という条件で「Japan travelers」という名前の
  セグメントを保存し、どの端末がこのセグメントに属するかを計算します。この手順は、ネットワーク
  越しの呼び出しではなく、配信対象解決とセグメント管理のパッケージを直接呼び出して行います。
  セグメントを定義する管理者向けの API を、
  [CW-0001](../../roadmaps/CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope-ja.md)
  がまだ用意していないためです。この欠落は、`AdminService` の
  [proto 定義](../../proto/citywalk/admin/v1/admin.proto) 自身にも書かれています。
- **メッセージの作成と公開**：ダイアログ形式のバリアントを1つ持つメッセージを、先ほどのセグメント
  を配信対象として `AdminService.CreateMessage` で作成します。続けて `AdminService.UpdateMessageState`
  で、メッセージを `draft` から `active` へ進めます。この遷移は
  [FR-MSG-01](../requirements.md) が定める、後戻りしない一方向の状態遷移です。最後に
  `AdminService.ListAuditLog` を呼び、誰がいつこの遷移をしたかをサーバーの記録から読み出します。
- **端末への同期**：`DeliveryService.Sync` を2回呼びます。1回目は事前状態なしで呼び、メッセージ
  を1件のエントリとして受け取ります。2回目は、1回目の応答が返した etag を添えて呼びます。すると
  `unchanged: true` とともに、空のエントリが返ります。この違いが、
  [CW-0006](../../roadmaps/CW-0006-payload-delta-sync/CW-0006-payload-delta-sync-ja.md) が定める
  条件付きリクエストです。内容に変化がない限り、端末は配信内容を組み立て直すコストを払いません。
- **確認とイベントの送信**：`DeliveryService.Confirm` を呼びます。端末がメッセージを表示する
  直前に、サーバー側で行う再確認を再現します。続けて `EventService.Submit` を呼び、`Sync` が配信
  したメッセージとバリアントに対するインプレッションイベントを記録します。

## うまくいかないとき

- `connect to postgres ...`：設定した接続文字列で PostgreSQL に到達できていません。
  `make demo-up` が完了しているかを確認してください。
  `docker compose -f deploy/docker-compose.yml ps` で、両方のサービスが healthy になっている
  はずです。ポート 5432 を他のプロセスがすでに使っている場合は、`-postgres-dsn` フラグに別の
  接続先を渡してください。
- `sync returned N entries, want 1`：`cmd/demo` は実行のたびに、自分のテーブルと Redis のキーを
  消してから始めます。それでもこのエラーが出るのは、同じ PostgreSQL データベースや Redis
  インスタンスに、他のプロセスが同時に書き込んでいる場合です。`make demo-down` に続けて
  `make demo-up` を実行し、空のデータベースからやり直してください。あわせて、`-postgres-dsn` と
  `-redis-addr` を他のプロセスと共有していないかも確認してください。
