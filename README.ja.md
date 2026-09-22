# agent-sandbox

[English](README.md) | **日本語**

AI コーディングエージェント (Claude Code) を [nono](https://github.com/tkancf/nono)
サンドボックス内で動かし、エージェントが発行するすべてのシェルコマンドを、
専用の兄弟 nono セッションで動くホスト側の exec デーモン execd 経由で仲介します。
それぞれのコマンドが何に触れてよいかは、オペレーターが書く nono の
コマンドプロファイルが決めます — `agent-sandbox.toml` ではありません。

目的はエージェントをマシンから締め出すことではなく、境界を*明示的で検査可能*に
することです。`agent-sandbox ai explain` は、どのコマンドが自分専用のサンドボックスで
動き、どれが execd 自身の権限で動き、拒否がなぜ起きたのかをエージェントに伝えます。
おかげでポリシー拒否が、再試行する価値のある原因不明の失敗ではなく、ポリシー拒否として
読まれます。

```
launcher
├── nono wrap  --profile <エージェントプロファイル>  -- claude …   ここにコマンド制御はない
└── nono run   --profile <コマンドプロファイル>      -- agent-sandbox execd
                                               │
                                               ├─ exec git → shim → git 専用の子サンドボックス
                                               │                     └─ exec ssh → shim → ssh 専用
                                               └─ exec rg  → execd 自身のサンドボックスで直接実行
```

## 必要なもの

- `PATH` の通った [nono](https://github.com/tkancf/nono) — サンドボックスエンジン本体
- Go 1.25 以降 (ソースからビルドする場合)
- `PATH` の通った `claude` — `agent-sandbox claude` を使う場合

`agent-sandbox doctor` で確認できます。

## インストール

```bash
go install github.com/ynny-github/agent-sandbox@latest
```

[mise](https://mise.jdx.dev/) を使う場合:

```toml
# .mise.toml
[tools]
"go:github.com/ynny-github/agent-sandbox" = "latest"
```

**コマンドプロファイルが書き込みを許可するどのパスの外にも置いてください** —
そうでないとセッションが起動しません。`go install` の既定の挙動がこれにあたります。

## クイックスタート

プロジェクトルートに `agent-sandbox.toml` を書きます。プロファイルのファイル名を
指すだけで、中身を生成することはありません。

```toml
[agents.claude]
profile = "claude-profile.json"
```

次に、2 つのプロファイルを nono 自身のスキーマで自分で書きます。
`claude-profile.json` がエージェントプロセス自身のサンドボックス、
`command-profile.json` が execd 経由で動くすべてのコマンドのサンドボックスです。
どちらにも既定値はなく、ファイルが無ければ起動エラーになります。このリポジトリ自身の
2 ファイルが実例で、各エントリにその理由がコメントで書かれています。

```bash
agent-sandbox doctor            # nono、execd ソケット、2 つのプロファイルは使えるか
agent-sandbox ai config-check   # agent-sandbox.toml は解決でき、両プロファイルは検証を通るか
agent-sandbox claude -- --model opus
```

`sandbox up` のような手順はありません。`agent-sandbox claude` が execd を専用の
nono セッションで起動し、2 つ目の兄弟セッションで Claude を起動し、Claude の終了時に
execd を片付けます。

## 仕組み

**兄弟セッションが 2 つ、入れ子にはしない。** 一方はエージェントプロファイルで
Claude Code を包み、もう一方はコマンドプロファイルで `agent-sandbox execd` を
動かします。サンドボックスは入れ子にできないので、execd はエージェントの
セッションの中では動きません。

**ループの中にシェルはいません。** エージェントが発行するコマンドは unix ソケットで
execd に届き、execd は行を自前のインタプリタで解釈し (パイプライン、
`&&`/`||`/`;`、リダイレクト、グロブ、`$(…)`、`for`/`if`、`cd` とその他のビルトインは
すべて動きます)、単純コマンドごとに直接 `execve` します。行を `bash -c` に渡す設計は
安全にできません — `git reset --hard` に対して書いた拒否は、git を名前ではなく
ストアパスで起動するだけで破られます。

**2 つの層。** コマンドプロファイルで宣言されたコマンドは自分専用の子サンドボックスを
持ち、生成された shim 経由でのみ到達されます。それ以外は execd 自身の権限で直接
動きます。前者の拒否は必ず理由を説明します — プロファイルのエントリが書いた `reason`
が拒否に載ります。後者の失敗は素の `execve` の権限エラーで、返す理由を持ちません。
execd 自身のビルトインは 3 つ目の扱いで、execd プロセスの中で動きます。つまり
自分で書いたリダイレクトやグロブは、くっついている相手のコマンドではなく execd の
権限で縛られます。

**どちらのプロファイルも agent-sandbox のものではありません。** プロファイルを一切
生成しません。2 つのファイルはあなたのもので、agent-sandbox はパスを解決して `nono` に
渡すだけです。実際に許可を決めるのは nono なので、スキーマと
`nono profile show` / `nono why` については
[nono 自身のドキュメント](https://github.com/tkancf/nono)を参照してください。

## コマンド

| コマンド | 内容 |
|---|---|
| `agent-sandbox claude -- [claude の引数...]` | execd を兄弟セッションで動かしつつ、nono 配下で Claude を起動 |
| `agent-sandbox exec -- <command>` | コマンドを 1 つ execd に送り、出力をストリームする |
| `agent-sandbox doctor` | 起動が依存するものを一通り確認: サンドボックスエンジン、execd ソケット、両プロファイルとそれが固定しているパス、そしてコマンドプロファイルが execd 自身のバイナリを書き込み可能にしていないこと。終了コード 0 / 1 |
| `agent-sandbox debug -- [claude の引数...]` | 実行はせずに、両セッション分の `nono` コマンドを表示 |
| `agent-sandbox ai explain` | エージェント向けのサンドボックス説明: コマンドの実行経路、2 つの層、拒否の理由 |
| `agent-sandbox ai config-check` | `agent-sandbox.toml` と 2 つの nono プロファイルを、起動時と同じ読み方で検証 |
| `agent-sandbox hook` | PreToolUse アダプタ (起動時に注入され、Claude が呼ぶもので、手で叩くものではない) |

グローバルフラグは `--config <path>` (既定 `agent-sandbox.toml`) と `--env <ref>`
(繰り返し可) の 2 つで、`claude` と `debug` はさらに `--context-mode` を受け取ります。
`--` の前に置けるのはこれらだけで、`--` 以降はすべて `claude` に渡ります。`--settings`
は PreToolUse フックを載せるために予約されており、パススルーとして拒否されます。

`--context-mode` は、そのセッションで
[context-mode](https://github.com/ynny-github/context-mode) の execd バックエンドを
選びます (`CONTEXT_MODE_EXEC_BACKEND=execd` をエージェントに渡す)。付けた場合、
`ctx_execute` が動かすコードは他のコマンドと同じく execd とコマンドプロファイルを
通ります。付けない場合、そのコードは MCP サーバーの子プロセスとしてエージェント
プロファイルの中で動きます。Claude Code が context-mode プラグインを有効と報告し、
かつ選択がサンドボックス内で実測できる場合にのみ起動します — 変数が届かないことは
context-mode 側ではエラーにならず、黙ってエージェントプロファイルでの実行に戻るためです。
エージェントプロファイルの `environment.allow_vars` に `CONTEXT_MODE_EXEC_BACKEND` が
必要で、`node` が実行できることも必要です。プラグインの有効状態と変数の転送は
`agent-sandbox doctor` が報告し、`node` の到達性は起動前の probe が実測します。

## 設定

```toml
command_profile = "command-profile.json" # 既定の名前。すべてのエージェントで共有

[agents.claude]
profile = "claude-profile.json"          # 既定の名前: "<agent>-profile.json"
```

Bash と Monitor は有効なままです。起動時に `claude --settings` で PreToolUse
フックを注入し、各コマンドを `agent-sandbox exec -- <command>` に書き換えます。
`.claude/settings.json` には何も書きません。`agent-sandbox` が `PATH` に必要です。
制御を渡す前に、ランチャーがプローブ用のペイロードでフックを 1 回実行し、コマンドが
書き換わって返ってこなければ起動を拒否します — Claude Code は起動できないフックを
非ブロッキングのエラーとして扱い、そのままコマンドを実行してしまうので、それは
機能低下ではなくバイパスだからです。

プロファイルのパスは、絶対パスで書かない限り `agent-sandbox.toml` の隣で解決されます。
任意の `~/.config/agent-sandbox/config.toml` はプロジェクト設定と合成され、
プロジェクト側が設定した値が勝ちます — 「どのプロジェクトも設定ファイルの隣に
`claude-profile.json` を置く」はユーザースコープに一度書けば済みます。

両プロファイルはセッション開始時に一度だけ読まれます。編集が効くのは次の
`agent-sandbox claude` からで、セッションの途中では効きません。

エージェントのシェルは、ホストの bash ではなくランチャーが生成する wrapper です。
Claude Code はツールのコマンドを stdin がソケットの状態で起動し、非対話の bash は
stdin のソケットを rshd/sshd 経由の起動と解釈して `~/.bashrc` を読みます — どちらの
プロファイルも許可していないファイルなので、wrapper がないとツールの結果すべてに
権限エラーの行が付きます。ランチャーは execd ソケットの隣に `norc-bash-<pid>`
(中身は `bash --norc --noprofile` だけ) を書き、`--read-file` で許可し、
`CLAUDE_CODE_SHELL` で名指しし、セッション終了時に削除します。エージェント
プロファイルの `environment.allow_vars` にこの変数を書かないと nono が剥がし、
Claude はホストの bash に戻ります。セッション自体は動きますが騒がしくなるので、
`agent-sandbox doctor` が実測します。

`--env <ref>` (現状 `file:` のみ) は dotenv サブセットのファイルをランチャー自身の
プロセスに読み込みます。**これは何も許可しません。** 転送されるのはプロファイルの
`environment.allow_vars` に並んでいるものだけなので、変数がエージェントに届くのは
エージェントプロファイルがその名前を書いている場合だけです。execd 経由のコマンドに
見せるのはコマンドプロファイル側の別の編集になります — ただし 1 つ例外があり、
`AGENT_SANDBOX_EXECD_SOCKET` はどんな名前でもワイルドカードでも、そこに書いては
いけません。execd ソケットに到達できるコマンドは execd に再帰でき、
execd は同時実行の上限なしにハンドラを起動します。

## 開発

```bash
mise install          # Go と lefthook
go test ./...         # ユニット + 統合テスト
go build ./...
mise run build        # `go install` でワーキングツリーのビルドを入れる
```

**`go build` や `go run .` ではなく `mise run build` を使ってください。**
バイナリをこのワーキングツリーの外かつ `PATH` の通った場所に置けるのは `go install`
だけで、起動にはそこに置かれている必要があります。

コミットは [Conventional Commits](https://www.conventionalcommits.org/) に従い、
`lefthook` が `commit-msg` でタイトルを検証します。

## ライセンス

[MIT](LICENSE) © Yuya Nagai
