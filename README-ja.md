[English](README.md) · **日本語**

# citywalk

このリポジトリは、citywalk のサーババックエンドの設計記録を置く場所です。置いてあるのは2種類の
ドキュメントだけで、アプリケーションのコードはまだありません。1つはバックエンドが満たすべき要件の
一覧、もう1つは設計上の決定を1件ずつ記録したロードマップ項目です。コードはこれらのドキュメントで
決めたことに従って、後から書きます。

citywalk は iOS と Android 向けのウォーキングアプリです。そのバックエンドが最初に扱う対象が
**アプリ内メッセージ配信基盤**です。利用者がアプリを使っている最中に、アプリ自身の画面へ重ねて、
文脈に応じたメッセージ（ダイアログ、バナー、全画面パネル）を描画する機能を指します。プッシュ通知が
アプリの外にいる利用者を呼び戻す手段であるのに対して、アプリ内メッセージはすでにアプリを開いている
利用者に届き、その瞬間の行動に応じて出し分けられます。

配信基盤を作るのは、作らない場合の代償がリリース1回分になるからです。アプリに作り込んだ施策は、
コードの変更とレビューを経て、インストール済みの端末の大半に行き渡るまで2週間を要します。文言の
誤りが判明しても2週間は直せず、止めたい施策も止められません。配信基盤はこの関係を逆転させます。
アプリが出荷するのは描画部だけで、以後の施策はすべてデータになります。

## ディレクトリ構成

```
docs/
  requirements.md      要件一覧（英語）。要件ごとに識別子を振ってあります
  ja/requirements.md   要件一覧（日本語）
roadmaps/
  README.md            ロードマップ項目の書き方と、これまでに書いた項目の一覧
  CW-0001-in-app-message-platform-scope/
    CW-0001-in-app-message-platform-scope.md      英語
    CW-0001-in-app-message-platform-scope-ja.md   日本語
.agent-workflows/      本リポジトリのドキュメントが従う文章規範、その textlint 実行環境、
                        ロードマップ項目をコードに変換する implement ワークフロー
.claude/skills/        これらのワークフローを Claude Code のスキルとして公開したもの
```

## 読む順序

はじめに [`docs/ja/requirements.md`](docs/ja/requirements.md) を読んでください。配信基盤が何を
満たすべきかだけを述べたもので、実現方法は扱いません。機能要件には `FR-<領域>-<番号>`、非機能要件
には `NFR-<領域>-<番号>` の形式で識別子を振ってあり、ロードマップ項目とテストケースの双方から
参照します。

次に
[CW-0001](roadmaps/CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope-ja.md)
を読んでください。配信基盤を5つのサービスへ分解する項目です。分解の軸は、各サービスが持つデータの
変化の速さに置いてあります。それぞれを設計する項目の番号も、あわせて示しています。

個々の項目へ進むときは
[`roadmaps/README-ja.md`](roadmaps/README-ja.md) を見てください。項目の一覧、各項目が従う書式、
新しい項目を追加する手順を示しています。

これらの項目の一部が説明するフェーズ1のサーバーを、使い捨ての PostgreSQL と Redis に対して動かせる
のが [`docs/ja/demo.md`](docs/ja/demo.md) です。現時点で公開しているネットワークの呼び出しを、
一通り試せます。

## 2言語での記述

本リポジトリのドキュメントは、すべて英語と日本語の両方で書きます。片方の言語への変更は、
もう片方への変更と同じ変更に含めます。

ファイルの命名規則はディレクトリごとに異なります。`docs/` 配下では、英語のファイルと同じパスを
`docs/ja/` の下に作り、そこへ日本語のファイルを置きます。`roadmaps/` の下では、日本語のファイルを
英語のファイルと同じ場所に、同じスラグへ `-ja` を付けた名前で置きます。各ファイルの先頭には、
対になるファイルへのリンクを言語切り替え行として置きます。

日本語のファイルは機械的な翻訳ではありません。日本語のファイルは、同じ論証を同じ構成で述べます。
ただし英語の文を1文ずつなぞるのではなく、日本語として自然に読める形に書き直します。

## 文章規範

本リポジトリのドキュメントは論証を含む文章であり、読者はそれぞれの文書が立てる論証を追えなければ
なりません。その文章の形を決める規範は、次の3層で構成しています。

- [`document-writing`](.agent-workflows/document-writing/workflow.md)：言語に依存しない原則
- [`english-document-writing`](.agent-workflows/english-document-writing/workflow.md)：英語の作法
- [`japanese-document-writing`](.agent-workflows/japanese-document-writing/workflow.md)：日本語の作法

規範は下書きの形を決めるためのものであって、書き上げた後の校正手順ではありません。下書きを
始める前に読んでください。

書き上げたら、変更したファイルに [textlint](https://github.com/textlint/textlint) をかけ、指摘が
ゼロになるまで推敲と再実行を繰り返します。実行環境と設定は
[`.agent-workflows/document-writing/textlint/`](.agent-workflows/document-writing/textlint/)
に置いてあり、node と npm が必要です。

```bash
SKILL_DIR=.agent-workflows/document-writing
npm --prefix "$SKILL_DIR/textlint" ci --ignore-scripts
npx --prefix "$SKILL_DIR/textlint" textlint \
  --config "$SKILL_DIR/textlint/.textlintrc.json" \
  docs/ja/requirements.md
```

## ロードマップ項目の実装

ロードマップ項目の `Detailed design` をコードに変換する作業には、専用のガードレール付きワークフロー
[`implement`](.agent-workflows/implement/workflow.md) があります。項目を特定し、変更内容を計画して
承認を待ってからコードを書き始め、
[CW-0010](roadmaps/CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack-ja.md) が定めた
技術スタックに沿って実装し、機械的なゲートと意味面でのセルフレビューを、指摘がなくなるまで繰り返して
から、項目の `Status` を更新して出荷します。

## 現在の状態

ロードマップ項目の状態はすべて `提案` です。設計は検討中で、コードはまだ出荷していません。設計の
前提は次の3点です。バックエンドの言語は Go とします。想定規模は月間アクティブ利用者数で 100 万から
1000 万とします。クライアントは、オフラインでも動作する iOS と Android のネイティブアプリとします。

メッセージを端末上で描画するクライアントの実装そのものは、本リポジトリの対象外です。本リポジトリが
定めるのは、配信基盤がそのクライアントに要求する責務です。
