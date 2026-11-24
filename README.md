# inliner

AI に書かせたローカルの HTML をブラウザで開き、**気になる箇所に直接コメントを付けて、そのまま AI に渡す**ためのツールです。

「3 つ目のカードの見出しが…」と場所を文章で説明する代わりに、テキストを選択してコメントを書くだけ。コメントは `ファイル:行番号` と引用付きの Markdown になるので、AI はどこをどう直せばいいかを取り違えません。

```
AI が HTML を書く → inliner で開いてコメント → AI が .inliner/feedback.md を読んで修正
        ↑                                                      │
        └──────── ページが自動リロードされ、コメントは追従 ────────┘
```

## インストール

Node.js 20 以上が必要です。依存パッケージはありません。

```sh
npm install -g github:ntk221/inliner
```

インストールせずに試すなら `npx github:ntk221/inliner page.html`。

## 使い方

AI が作業しているディレクトリ(プロジェクトのルート)で実行します。

```sh
inliner out/report.html     # ファイルを開く
inliner                     # カレントディレクトリの HTML 一覧から選ぶ
```

ブラウザが開くので、コメントを付けます。

| やりたいこと | 操作 |
| --- | --- |
| 文章にコメント | テキストを選択 →「コメント」 |
| 画像・ボタン・ブロックにコメント | パネルの「要素を選択」→ 対象をクリック |
| ページ全体へのコメント | パネルの「ページ全体」 |
| 保存 | `⌘/Ctrl + Enter` |

元の HTML ファイルは一切書き換えません。コメントは `.inliner/` に保存されます。

### AI に渡す

方法は 2 つあります。

**1. ファイルを読ませる(おすすめ)** — 未解決のコメントは常に `.inliner/feedback.md` に書き出されています。AI にはこう伝えるだけです。

> `.inliner/feedback.md` のコメントを反映して

**2. コピーして貼る** — パネルの「AI用にコピー」で同じ内容がクリップボードに入ります。チャット型の AI に貼り付ける場合はこちら。

`feedback.md` の中身はこうなります。

```md
## out/report.html

### #1 · `out/report.html:17` · text

- Selected text: "driven by new customers"
- In context: "…grew strongly this quarter, [[driven by new customers]]. Summary Revenue…"
- Inside: `<p class="lead">` (selector `body > header > p`)

> 抽象的すぎる。上位 2 社の社名を入れて。
```

AI がファイルを書き換えるとページは自動でリロードされ、コメントは選択したテキストを手がかりに新しい位置へ追従します(行番号も更新されます)。対象のテキスト自体が消えた場合は「該当箇所が見つかりません」と表示されるので、直ったことを確認して解決にしてください。

### AI に解決済みにしてもらう

AI は対応が終わったコメントを自分で解決済みにできます。メモはパネルに表示されます。

```sh
inliner resolve 1 -m "Acme と Globex の社名を追記"
```

`CLAUDE.md` や `AGENTS.md` に次のように書いておくと、毎回説明せずに済みます。

```md
## HTML のレビューコメント

- HTML へのレビューコメントは `.inliner/feedback.md` にある。「コメントを反映して」と言われたらこれを読む。
- 1 件対応するごとに `inliner resolve <id> -m "何を変えたか"` を実行する。
```

## コマンド

```
inliner [path]              HTML ファイルまたはディレクトリをレビュー用に開く(省略時は .)
inliner show [file]         未解決コメントを Markdown で表示(feedback.md と同じ内容)
inliner resolve <id...>     解決済みにする(-m "メモ")
inliner reopen <id...>      未解決に戻す
inliner rm <id...>          削除する
inliner clear               解決済みコメントを削除(--all ですべて削除)

  -p, --port <n>     ポート(既定 4600。使用中なら次の空きポート)
      --no-open      ブラウザを開かない
  -C, --root <dir>   プロジェクトのルート(.inliner/ の場所。既定はカレントディレクトリ)
      --all          show: 解決済みも表示
      --json         show: JSON で出力
```

## 仕組みと注意点

- **ローカル専用のサーバー**です。`127.0.0.1` だけで待ち受け、`localhost` 宛て以外のリクエストや他サイトからの書き込みは拒否します。`.git` や `.env` などドットで始まるファイルは配信しません。
- 配信時に、各タグへソースの行番号(`data-inliner-line` 属性)を付け、コメント UI のスクリプトを 1 つ挿入します。変更するのはブラウザに送る内容だけで、ディスク上のファイルはそのままです。UI は Shadow DOM の中に描画するので、ページのスタイルとは干渉しません。
- 行番号は「選択箇所を含む要素の開始タグの行」です。スクリプトで動的に生成された要素は、ソース上にある直近の親要素の行になります。
- `.inliner/` には自動で `.gitignore` が作られ、コミット対象になりません。
- 対応ブラウザは Chrome / Edge / Safari 17.2 以降 / Firefox 140 以降(テキストのハイライトに CSS Custom Highlight API を使用)。

## 開発

```sh
npm test            # ユニットテスト
npm run test:e2e    # 実ブラウザでの通しテスト(Playwright が入っている場合のみ実行)
```
