[English](README.md) · **日本語**

# citywalk ロードマップ

このディレクトリは、citywalk のサーババックエンドの設計記録を置く場所です。各項目は1つの決定を、
何を作るのか、なぜそうするのか、何を採らなかったのかという形で記録します。アーキテクチャ決定記録
（Architecture Decision Record、以下 ADR）と同じ役割です。その場にいなかった人が数年後に
「なぜこの作りなのか」を読み解けることを目的とします。

各項目を **CW 項目**と呼びます。**CW** は *citywalk Evolution* の略です。項目ごとに4桁ゼロ埋めの
識別子を持ちます。識別子を変更することはなく、再利用もしません。

## ディレクトリ構成

1項目 = 1ディレクトリとし、`roadmap/` の直下へフラットに並べます。

```
roadmap/
  CW-0001-in-app-message-platform-scope/
    CW-0001-in-app-message-platform-scope.md      ← 英語
    CW-0001-in-app-message-platform-scope-ja.md   ← 日本語
```

両言語のファイルは同じ識別子と同じスラグを持ちます。日本語ファイルは機械的な翻訳ではありません。
`#` 見出しは日本語で書き、本文も英語の文を1文ずつなぞるのではなく、日本語として自然に読める形に
書き直します。

## 項目の追加手順

1. **次の識別子を採番します**。`roadmap/` 配下の全ディレクトリを見て、既存の最大 `CW-NNNN` に1を
   足した番号を使います。再利用、飛ばし、当て推量のいずれも行いません。

   ```bash
   ls -d roadmap/CW-*/ | sort | tail -1
   ```

2. **ディレクトリと両言語のファイルを作成します**。状態は `提案` から始めます。新しい項目は必ず
   提案として生まれます。

3. **文章規範に従って本文を書きます**。下書きを始める前に
   [`document-writing`](../.agent-workflows/document-writing/workflow.md) スキルを読み込み、英語の
   ファイルには [`english-document-writing`](../.agent-workflows/english-document-writing/workflow.md) を、
   日本語のファイルには
   [`japanese-document-writing`](../.agent-workflows/japanese-document-writing/workflow.md) を
   併用します。規範は下書きの形を決めるためのものであり、書き上げた後の校正手順ではありません。

4. **両言語のファイルに textlint をかけます**。指摘がゼロになるまで推敲と再実行を繰り返します。
   実行環境と設定は
   [`.agent-workflows/document-writing/textlint/`](../.agent-workflows/document-writing/textlint/)
   に置いてあります。

識別子は永続します。状態が変わっても、実装が出荷されても、後続の項目に置き換えられても、項目は
同じ番号を持ち続けます。

## 書式

各ファイルは、言語切り替え行、`#` 見出し、メタデータブロックの順に始まり、続いて6つの節を
この順序で置きます。

| 節 | 内容 |
|---|---|
| はじめに | 項目が何を提案するのかを冒頭で述べます |
| 動機 | 問題、背景、その問題が重要な理由を述べます |
| 詳細設計 | 設計を、漏れなく重複なく分解して述べます |
| 検討した代替案 | 採らなかった選択肢を、採らなかった理由とともに述べます |
| 進捗 | 詳細設計の分解に対応するチェックリストを、作業単位ごとに1つ置きます |
| 参考 | リンクと出典を、読者がそこから何を得られるかの一文とともに並べます |

メタデータブロックは、機械的に読み取れるよう区切りを付けます。

```markdown
<!-- CW-METADATA -->
| 項目 | 値 |
|---|---|
| 提案 | [CW-0001](CW-0001-<slug>-ja.md) |
| 提案者 | [@handle](https://github.com/handle) |
| 状態 | **提案** |
| トピック | 配信モデル |
| 関連 | [CW-0002](../CW-0002-<slug>/CW-0002-<slug>-ja.md) |
<!-- /CW-METADATA -->
```

`関連` と `後継` は任意の欄で、相互に張ります。ある項目が別の項目を置き換えるときは、置き換える側が
古い項目を `関連` に並べ、古い側は後継を `後継` に書きます。

### 状態

`状態` は、その項目がどこまで進んでいるかについての唯一の拠り所です。ディレクトリのパスは状態に
依存しないため、状態が変わってもファイルは動かず、リンクも壊れません。

| 状態 | 意味 |
|---|---|
| 提案 | 検討中で、実装はまだありません |
| 実装中 | 採択され、実装が進行しています |
| 実装済み | 出荷済みです |
| 提案（保留） | 意図的に止めてあります |

状態を決めるのはコードです。実装を伴わずに書かれた項目は `提案` とします。コードを出荷する変更が
`状態` を `実装済み` へ、部分的な出荷なら `実装中` へ更新します。あわせて対応する `進捗` のチェックを
付け、同じ変更のなかでプルリクエストを記録します。すでにコードが出荷された項目に `提案` を残しません。

### トピック

`トピック` は項目を閲覧しやすくまとめるための欄です。現在使っているトピックは次のとおりです。

| トピック | 扱う範囲 |
|---|---|
| 配信モデル | メッセージが端末へ届く経路と、何を見せるかを誰が決めるか |
| ターゲティング | オーディエンスの述語式、セグメント、実験の割当 |
| 表示統制 | 表示頻度の上限、優先度、競合の解決 |
| 効果測定 | イベントの収集、集計、レポート |
| プラットフォーム | ランタイム、ストレージ、横断的な基盤 |

## 項目一覧

| ID | 項目 | トピック |
|---|---|---|
| [CW-0001](CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope-ja.md) | アプリ内メッセージ配信基盤のスコープと分解 | プラットフォーム |
| [CW-0002](CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model-ja.md) | ハイブリッド配信：オーディエンスはサーバ、トリガーは端末で解決する | 配信モデル |
| [CW-0003](CW-0003-message-definition-schema/CW-0003-message-definition-schema-ja.md) | メッセージ定義スキーマとその進化 | 配信モデル |
| [CW-0004](CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine-ja.md) | オーディエンス述語式の評価エンジン | ターゲティング |
| [CW-0005](CW-0005-segment-membership-index/CW-0005-segment-membership-index-ja.md) | セグメントのメンバーシップ索引 | ターゲティング |
| [CW-0006](CW-0006-payload-delta-sync/CW-0006-payload-delta-sync-ja.md) | 配信ペイロードの同期 | 配信モデル |
| [CW-0007](CW-0007-display-governance/CW-0007-display-governance-ja.md) | 表示統制：上限、優先度、競合の解決 | 表示統制 |
| [CW-0008](CW-0008-deterministic-experiment-assignment/CW-0008-deterministic-experiment-assignment-ja.md) | 実験とホールドアウトの決定的な割当 | ターゲティング |
| [CW-0009](CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics-ja.md) | イベント収集と効果測定のパイプライン | 効果測定 |
| [CW-0010](CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack-ja.md) | ランタイムと技術スタック | プラットフォーム |

## 関連ドキュメント

- [`docs/ja/requirements.md`](../docs/ja/requirements.md)：これらの項目が設計の対象とする要件の
  一覧です。要件ごとに識別子を振ってあります。
