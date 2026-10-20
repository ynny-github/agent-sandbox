# agent-sandbox

[English](README.md) | **日本語**

AI コーディングエージェント (Claude Code) を [nono](https://github.com/tkancf/nono)
サンドボックス内で動かし、エージェントが発行するすべてのシェルコマンドを、
専用の兄弟 nono セッションで動くホスト側の exec デーモン execd 経由で仲介します。
それぞれのコマンドが何に触れてよいかは、オペレーターが書く nono の
コマンドプロファイルが決めます。

目的はエージェントをマシンから締め出すことではなく、境界を*明示的で検査可能*に
することです。`agent-sandbox ai explain` は、どのコマンドが自分専用のサンドボックスで
動き、どれが execd 自身の権限で動き、拒否がなぜ起きたのかをエージェントに伝えます。
おかげでポリシー拒否が、再試行する価値のある原因不明の失敗ではなく、ポリシー拒否として
読まれます。

```
agent-sandbox launch claude → .agent-sandbox/bin/claude (あなたのスクリプト)
├── nono run   --profile <エージェントプロファイル>  -- claude …   ここにコマンド制御はない
└── execd up → nono run --profile <コマンドプロファイル> -- execd serve
                                               │
                                               ├─ exec git → shim → git 専用の子サンドボックス
                                               │                     └─ exec ssh → shim → ssh 専用
                                               └─ exec rg  → execd 自身のサンドボックスで直接実行
```

## 必要なもの

- `PATH` の通った [nono](https://github.com/tkancf/nono) — サンドボックスエンジン本体
- Go 1.25 以降 (ソースからビルドする場合)
- `PATH` の通った `claude` — `init` が書き出す起動スクリプト `claude` を使う場合

起動に失敗したときやコマンドが拒否されたときは、nono に直接聞いてください。
読み込めないプロファイルには `nono profile validate <profile>`、プロファイルが何を
許可しているかには `nono why --profile <profile> --path <p> --op read|write`
(または `--command <name>`) を使います。

## インストール

```bash
go install github.com/ynny-github/agent-sandbox/cmd/...@latest
```

`agent-sandbox` と `execd` の両方が入ります。起動スクリプトはどちらも `PATH` から実行します。

[mise](https://mise.jdx.dev/) を使う場合:

```toml
# .mise.toml
[tools]
"go:github.com/ynny-github/agent-sandbox/cmd/agent-sandbox" = "latest"
"go:github.com/ynny-github/agent-sandbox/cmd/execd" = "latest"
```

**コマンドプロファイルが書き込みを許可するどのパスの外にも置いてください** —
そうでないとセッションが起動しません。`go install` の既定の挙動がこれにあたります。

## クイックスタート

プロジェクトのルートで `agent-sandbox init` を実行します。バイナリに埋め込まれた
出発点一式 —— `.agent-sandbox/`(起動スクリプト、フックの設定、
shell wrapper、最小のコマンドプロファイルとエージェントプロファイル)と、
プロファイルの育て方を説明する 2 つの skill —— を書き出します。既存の
ファイルは決して上書きしません。存在するファイルは報告してそのまま残します。

```bash
agent-sandbox init
```

書き出されるのは出発点であって、完成した境界ではありません。コマンド
プロファイルはコマンドに `$WORKDIR` の読み書き、GitHub を含む `allow_domain`
リスト、6 つの環境変数、そして `git` と `ssh` のポリシーを与えます ——
`ssh` は `git` 経由でのみ到達できます。必要に応じて広げてください。同梱される
`growing-a-nono-profile` skill が変更の手順を案内します。

**NixOS ではコマンドプロファイルに 1 箇所の編集が必要です。** 実行ファイルは
`/nix/store` にあり、nono の既定の床はそこを許可しないため、次を追加するまで
すべてのコマンドが exit 127 で失敗します。

```json
"groups": { "include": ["nix_runtime"] },
"filesystem": { "read": ["/nix/store", "/run/current-system/sw"] },
```

あわせて `git --exec-path` の出力を git の `exec_paths` と `fs_read` の両方に追加して
ください（子サンドボックスがヘルパーを実行するには両方が必要です）。
エージェントプロファイルは影響を受けません —— ベースの `nolabs-ai/claude` が既に
`nix_runtime` を含んでいます。

**エージェントプロファイルはレジストリの pack を extends します。** テンプレートも
このリポジトリ自身の `.agent-sandbox/claude-profile.json` も `nolabs-ai/claude` を extends して
おり、nono 0.78 にはこれが組み込まれていません。初回起動の前に一度インストール
してください。

```bash
nono pull nolabs-ai/claude
```

pull すると、pack の Claude Code プラグイン `nono@nolabs-ai` も
`~/.claude/settings.json` で有効になります。`nono remove nolabs-ai/claude` で
両方とも元に戻ります。

両方のプロファイルを nono のスキーマで一から書くこともできます。このリポジトリ
自身の `.agent-sandbox/` は時間をかけて育てたプロファイルの実例で、各エントリが
その理由をコメントに持っています。`templates/minimal/.agent-sandbox/` は `init` が
配る出発点です。

```bash
agent-sandbox launch claude            # Claude を起動。後ろの引数はスクリプトに渡る
agent-sandbox launch claude --model opus
```

`.agent-sandbox/bin/claude` はあなたのシェルスクリプトです。`agent-sandbox doctor`
を実行し、`execd up` で execd を起動し、フックの設定を付けて `nono run` の下で
Claude を起動し、Claude の終了時に `execd down` で execd を止めます。自由に編集
してください —— herdr や tmux、追加のフラグ、別のエージェント用のスクリプトを
`bin/` に足すこともできます。

起動は必ず `agent-sandbox launch` を通してください。作業ディレクトリ以下のファイル
—— `.agent-sandbox/`、プロファイル、`.git` まで —— はエージェントが実行するコマンドから
書き換えられるので、起動と起動の間にスクリプトを書き換えられる可能性があります。
`launch` は `.agent-sandbox/` を、前回承認して `~/.config/agent-sandbox/trusted/` に
控えた写しと比べ、違いがあれば差分を表示して端末で確認し、承認済みの写しを実行します。
プロジェクトでの初回の起動は比べる写しがないので、その時点の `.agent-sandbox/` を
確認なしで承認します —— clone したリポジトリは、初回の起動の前に `.agent-sandbox/`
を読んでください。
`.agent-sandbox/bin` を `PATH` に通さないでください。承認を経ずに実行されるうえ、
`claude` という名前のスクリプトは自分自身を呼び続けます。近道は、自分のシェルの
設定ファイルに置くエイリアスです。

```bash
alias claude-sb='agent-sandbox launch claude'
```

## 仕組み

**兄弟セッションが 2 つ、入れ子にはしない。** 一方はエージェントプロファイルで
Claude Code を包み、もう一方はコマンドプロファイルの execd で、`execd up` が
起動します。サンドボックスは入れ子にできないので、execd はエージェントの
セッションの中では動きません。

**行は bash が実行し、ポリシーは nono が適用します。** エージェントが発行する
コマンドは unix ソケットで execd に届き、execd は行を自分の sandbox の中で
`bash --norc --noprofile -c` で実行します。nono は各コマンドのポリシーを実行の時点で
適用します —— 名前でも、パスでも、`env`・`sh -c`・関数経由でも同じです (nono 0.78.0
で実測)。そのため、誰が行を解釈しても、効くポリシーは変わりません。制限が 1 つ
あり、nono は短時間に約 5 回までしかポリシー管理下のコマンドの起動を受け付けない
ので、そうしたコマンドをループで何度も呼ぶ行は、`supervisor.sock` に接続できず
exit 126 になることがあります。その場合は分けて実行してください。

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
| `agent-sandbox init` | バイナリに埋め込まれた `.agent-sandbox/` とプロファイル用 skill をこのプロジェクトに書き出す。上書きはしない |
| `agent-sandbox launch <name> [引数...]` | `.agent-sandbox/` の変更を承認したうえで `.agent-sandbox/bin/<name>` を実行する。実行されるのは承認済みの写し |
| `agent-sandbox doctor` | 起動が安全かを確認: フックがエージェントプロファイルで動くこと、コマンドプロファイルが `execd`(とその置き場所)と `agent-sandbox` のどちらも書き込み可能にしていないこと、エージェントプロファイルが `launch` の実行する承認済みの写しに書き込めないこと。起動スクリプトが最初に実行する。`--command-profile`/`--agent-profile` の既定は `.agent-sandbox/`。終了コード 0 / 1 |
| `agent-sandbox ai explain` | エージェント向けのサンドボックス説明: コマンドの実行経路、2 つの層、拒否の理由 |
| `agent-sandbox ai pointer` | 起動スクリプトが Claude に渡すシステムプロンプトの一文 |

`execd` は独立したバイナリで、設定ファイルを持ちません。

| コマンド | 内容 |
|---|---|
| `execd up --profile <path> [--workdir <dir>]` | このシェル・workdir・プロファイルのセッションを nono の下で起動する(動いていればそれを使う)。ソケットを出力する。起動スクリプトが実行する |
| `execd down [<socket>]` | そのセッションを止めてディレクトリを消す。socket を省くと、このシェルが起動したセッションをすべて止める。起動スクリプトが終了時に実行する |
| `execd gc [--idle <duration>] [--dry-run]` | 死んでいる・シェルが消えた・`--idle`(既定 168h)より長く使われていないセッションを片付ける |
| `execd exec [--timeout d] -- <command>` | `AGENT_SANDBOX_EXECD_SOCKET` のソケットにコマンドを 1 つ送り、出力をストリームする |
| `execd hook <agent>` | エージェントのツールフック: シェルコマンドを `execd exec -- …` に書き換える。今あるのは `claude` だけ。`.agent-sandbox/settings.json` に書かれ、Claude が呼ぶもので、手で叩くものではない |

セッションは `$XDG_STATE_HOME/execd/`(`~/.local/state/execd/`)に置かれます。起動スクリプトが kill されるなどで `execd down` まで届かなかったセッションは、`execd gc` が片付けるまで動き続けます。

[context-mode](https://github.com/ynny-github/context-mode) は、`ctx_execute` が
動かすコードを、エージェントプロファイルの中ではなく execd とコマンドプロファイル
経由で実行できます。起動スクリプトの `export CONTEXT_MODE_EXEC_BACKEND=execd` の
コメントを外してください。変数が届かないことはエラーにならず、黙ってエージェント
プロファイルでの実行に戻るので、エージェントプロファイルの
`environment.allow_vars` に `CONTEXT_MODE_EXEC_BACKEND` が必要で、`node` が実行
できることも必要です。

## 設定

```
.agent-sandbox/
  bin/claude             起動スクリプト (あなたのもの。他のエージェント用に追加してよい)
  settings.json          `claude --settings` に渡す PreToolUse フックの設定
  bash                   CLAUDE_CODE_SHELL に使う shell wrapper
  command-profile.json   execd のプロファイル
  claude-profile.json    エージェントのプロファイル
```

設定ファイルはありません。Bash と Monitor は有効なままで、`settings.json` のフックが
各コマンドを `execd exec -- <command>` に書き換えます。`.claude/settings.json` には
何も書きません。`agent-sandbox` と `execd` が `PATH` に必要です。起動スクリプトが
最初に実行する `doctor` が、フックがエージェントプロファイルで動くことを確かめます
—— Claude Code は起動できないフックを非ブロッキングのエラーとして扱い、そのまま
コマンドを実行してしまうので、それは機能低下ではなくバイパスだからです。

セッションは承認済みの写しから動くので、`.agent-sandbox/` を編集しても動いている
セッションは変わりません。次の `launch` が変更を表示して確認します。

エージェントのシェルは、ホストの bash ではなく `.agent-sandbox/bash` です。
Claude Code はツールのコマンドを stdin がソケットの状態で起動し、非対話の bash は
stdin のソケットを rshd/sshd 経由の起動と解釈して `~/.bashrc` を読みます — どちらの
プロファイルも許可していないファイルなので、wrapper がないとツールの結果すべてに
権限エラーの行が付きます。起動スクリプトが `--read-file` で許可し、
`CLAUDE_CODE_SHELL` で名指しします。エージェントプロファイルの
`environment.allow_vars` にこの変数を書かないと nono が剥がし、Claude はホストの
bash に戻ります。セッション自体は動きますが騒がしくなります。

転送されるのはプロファイルの `environment.allow_vars` に並んでいるものだけなので、
変数がエージェントに届くのはエージェントプロファイルがその名前を書いている場合
だけです。execd 経由のコマンドに見せるのはコマンドプロファイル側の別の編集になります
— ただし 1 つ例外があり、`AGENT_SANDBOX_EXECD_SOCKET` はどんな名前でもワイルド
カードでも、そこに書いてはいけません。execd ソケットに到達できるコマンドは execd に
再帰でき、execd は同時実行の上限なしにハンドラを起動します。

## 開発

```bash
mise install          # Go と lefthook
go test ./...         # ユニット + 統合テスト
go build ./...
mise run build        # `go install` で両バイナリのワーキングツリービルドを入れる
```

**`go build` や `go run ./cmd/agent-sandbox` ではなく `mise run build` を使ってください。**
バイナリをこのワーキングツリーの外かつ `PATH` の通った場所に置けるのは `go install`
だけで、起動にはそこに置かれている必要があります。

`templates/minimal/` と 2 つのプロファイル用 skill は、`agent-sandbox init` が書き出す
ものです。バイナリに埋め込まれている (モジュールのルートの `embed.go`) ので、
バイナリが書き出すひな形は常にそのバイナリと一致し、変更は利用者が入れ直したときに
届きます。`go test ./...` がひな形のプロファイルに対して `nono profile validate` を
実行します。このリポジトリ自身の `.agent-sandbox/` と混同しないでください。あちらはこのホスト向けに育てたもので、可搬ではありません。

コミットは [Conventional Commits](https://www.conventionalcommits.org/) に従い、
`lefthook` が `commit-msg` でタイトルを検証します。

## ライセンス

[MIT](LICENSE) © Yuya Nagai
