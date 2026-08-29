# agent-sandbox

[English](README.md) | **日本語**

AI コーディングエージェント (Claude Code) をサンドボックス内で動かし、エージェントが
実行するシェルコマンドを **ホスト** / **コマンドごとの
[nono](https://github.com/tkancf/nono) サンドボックス** / **実行拒否** の
3 つの行き先に、TOML で書いたポリシーに従って振り分けます。

目的はエージェントをマシンから締め出すことではなく、境界を *明示的に、検査可能に*
することです。`agent-sandbox ai config-check` を実行すれば、サンドボックス内の
コマンドが到達できるパスと通信できるドメインが、実際に起動時に使われる設定から
解決されて表示されます。

```
                       agent-sandbox.toml
                              │
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
    allow_commands      drop_commands        (それ以外すべて)
          │                   │                   │
          ▼                   ▼                   ▼
   ┌─────────────┐     ┌─────────────┐     ┌──────────────────┐
   │   ホスト    │     │    拒否     │     │  nono run        │
   │  (直接実行) │     │  (exit 1)   │     │  CWD にスコープ  │
   └─────────────┘     └─────────────┘     └──────────────────┘
```

## 目次

- [必要なもの](#必要なもの)
- [インストール](#インストール)
- [クイックスタート](#クイックスタート)
- [仕組み](#仕組み)
- [コマンド](#コマンド)
- [設定](#設定)
  - [`tool_mode`](#tool_mode)
  - [ホストアクセス: 3 つのセクション](#ホストアクセス-3-つのセクション)
  - [capability](#capability)
  - [コマンドルーティング](#コマンドルーティング)
  - [ネットワーク](#ネットワーク)
  - [ユーザースコープ設定](#ユーザースコープ設定)
- [環境変数 (`--env`)](#環境変数---env)
- [GitHub MCP](#github-mcp)
- [safe ラッパー](#safe-ラッパー)
- [開発](#開発)
- [ライセンス](#ライセンス)

## 必要なもの

- `PATH` の通った [nono](https://github.com/tkancf/nono) — サンドボックスエンジン本体
- Go 1.25 以降 (ソースからビルドする場合)
- `PATH` の通った `claude` — `agent-sandbox claude` を使う場合

`agent-sandbox doctor` で確認できます。

## インストール

```bash
go install github.com/ynny-github/agent-sandbox/agent-sandbox@latest
```

[mise](https://mise.jdx.dev/) を使う場合:

```toml
# .mise.toml
[tools]
"go:github.com/ynny-github/agent-sandbox/agent-sandbox" = "latest"
```

## クイックスタート

プロジェクトルートに `agent-sandbox.toml` を書きます:

```toml
tool_mode = "hook"

[sandbox.shared]
capabilities = ["go", "python"]

[sandbox.agent]
capabilities = ["ssh"]          # ホストの認証情報 — エージェント専用。サンドボックスには渡らない
allow_commands = ["go *"]       # ホストで直接実行する
drop_commands = [
  { pattern = "gh *", message = "gh is disabled; use the GitHub MCP tools." },
]
```

設定が解決できることを確認してから起動します:

```bash
agent-sandbox doctor            # nono とブローカーソケットは使えるか
agent-sandbox ai config-check   # 設定は解決できるか、そして何を許可しているか
agent-sandbox claude -- --model opus
```

`sandbox up` のような事前準備は不要です。`agent-sandbox claude` がホスト側の
コマンドブローカーを起動し、nono の下で Claude を立ち上げ、Claude の終了時に
ブローカーを片付けます。

## 仕組み

### ルーティング

エージェントが実行するコマンドは、次の順にポリシーと照合されます。

1. **allow** — `sandbox.agent.allow_commands` にマッチ → シェル安全性の検証を経て
   **ホストで実行**
2. **drop** — `sandbox.agent.drop_commands` にマッチ → **拒否**。ホストでも
   サンドボックスでも実行されず、exit code 1 と stderr の 1 行 (ルールの `message`、
   未指定なら既定の `dropped: command matches drop pattern "<pattern>"`) を返す
3. **sandbox** — それ以外すべて → コマンドブローカーへ送られ、カレントワーキング
   ディレクトリにスコープされた専用の `nono run` の下で実行される

**allow が drop に優先します。** 両方にマッチするコマンドはホストで実行されます。
パターンは `*` が任意長の文字列にマッチするグロブで、前後両端がアンカーされます
(`go *` は `go test ./...` にマッチしますが `cd x && go test` にはマッチしません)。

次の 2 つは常にホスト許可で、TOML には書かれません: `agent-sandbox ai *`
(エージェントが自身の環境ドキュメントを読めるように) と `agent-sandbox safe *`
(safe ラッパーは実行前に検証するため)。

### ファイルシステムは仮想化されない

コンテナも bind マウントもありません。サンドボックス内のコマンドは、ホストの
ファイルシステム上で *同じ絶対パス* のまま実行され、カレントワーキングディレクトリ
(読み書き可) に制限されます。`HOME` は実際の値のままです。ホストコマンドと
サンドボックスコマンドの間でパスを変換する必要は一切ありません。

ワーキングディレクトリの外は、設定で明示的に許可した範囲だけが見えます。

### 2 つのプロファイル、相互継承なし

`agent-sandbox` は nono プロファイルを 2 つ生成します。起動されるエージェント用と、
ブローカー経由の各コマンドが動くシェルサンドボックス用です。

**2 つの間には一切の継承がありません。** ある許可がサンドボックスに届くのは、それが
そのサンドボックスから見える場所に書かれているときだけです。だからこそ許可を
*引き算* する手段は存在しません。「これはサンドボックスコマンドには渡したくない」は、
共通ベースではなく `[sandbox.agent]` に書くことで表現します。`[sandbox.shared]` に
書き忘れればコマンドサンドボックスはその許可を得られませんが、コマンドサンドボックスが
知らないうちに許可を *獲得する* ことは起こりません。

## コマンド

| コマンド | 内容 |
|---|---|
| `agent-sandbox claude -- [claude の引数...]` | コマンドブローカーを起動した状態で、nono 下に Claude を立ち上げる |
| `agent-sandbox exec -- <command>` | コマンドを 1 つルーティングして実行し、出力をストリームする |
| `agent-sandbox doctor` | `nono` が動作するか、ブローカーソケットが bind できるかを確認。exit 0 / 1 |
| `agent-sandbox debug -- [claude の引数...]` | 実行はせずに、組み立てられる `nono` コマンド、生成される 2 つのプロファイル、GitHub MCP 設定 (トークンは伏字) を表示 |
| `agent-sandbox ai explain` | 現在のサンドボックス環境の、エージェント向け説明 |
| `agent-sandbox ai config-check` | 起動時と同じ手順で設定を検証し、サンドボックスコマンドの到達範囲を表示 |
| `agent-sandbox safe git [引数...]` | 危険と分かっている呼び出しを拒否したうえで git を実行 |
| `agent-sandbox safe docker-compose [引数...]` | 解決済みプロジェクトを検証したうえで docker compose を実行 |
| `agent-sandbox command-router` | MCP サーバーを起動 (`tool_mode = "mcp"`) |
| `agent-sandbox hook` | PreToolUse アダプタ (`tool_mode = "hook"`。Claude が呼ぶもので、手で叩くものではない) |

グローバルフラグ: `--config <path>` (既定 `agent-sandbox.toml`) と `--env <ref>`
(繰り返し指定可)。

`claude` と `debug` では、`--` の前に置けるのは `--config` と `--env` だけです。
`--` の後ろはすべて `claude` に渡ります。`agent-sandbox` は `nono` にオプションを
転送しません — プロファイルは設定ファイルから生成されます。

`--settings` は `agent-sandbox` が予約しており、パススルーオプションとして拒否
されます。GitHub MCP が有効なときは `--mcp-config` / `--strict-mcp-config` も
拒否されます。

### `doctor`

`doctor` は起動が依存する 2 点を確認します。

- `nono` が `PATH` にあり、`nono --version` が動作すること
- コマンドブローカーが、ソケットディレクトリ (`$XDG_STATE_HOME/agent-sandbox`、
  未設定なら `~/.local/state/agent-sandbox`) で実際に **unix ソケットを bind
  できる** こと。単なる書き込みチェックでは不十分で、bind することで
  `sun_path` の約 104 バイト制限も検出できます

どちらかが失敗する場合、`agent-sandbox claude` は Claude を起動しません。

## 設定

### `tool_mode`

エージェントのコマンドがルーターに届く経路を選びます。

| モード | 挙動 |
|---|---|
| `hook` | Bash と Monitor は有効のまま。起動時に `claude --settings` 経由で PreToolUse フックが注入され、各コマンドが `agent-sandbox exec -- <command>` に書き換えられる。`.claude/settings.json` には何も書き込まれない。`agent-sandbox` が `PATH` にある必要がある |
| `mcp` (既定) | Bash と Monitor を無効化。エージェントは `run_command` MCP ツール経由でコマンドを実行し、出力は `mcp.command_output_dir` 配下のファイルに書かれる — レスポンスにはパスと exit code のみが載る |

`hook` モードでは、コマンドは **起動時に凍結されたポリシースナップショット** を
通ってルーティングされます。そのため、セッション中に `agent-sandbox.toml` を
編集しても実行中セッションのポリシーは変わりません。編集は次回の
`agent-sandbox claude` から反映されます。

```toml
tool_mode = "hook"

[mcp]
command_output_dir = "/tmp/mcp-output"  # mcp モードでは必須。hook モードでは無視される
```

### ホストアクセス: 3 つのセクション

| セクション | 適用先 |
|---|---|
| `[sandbox.shared]` | 起動されるエージェントとシェルサンドボックスの両方 |
| `[sandbox.agent]` | 起動されるエージェントのみ |
| `[sandbox.shell]` | シェルサンドボックス (ブローカー経由のコマンド) のみ |

3 つとも、同じ 6 つのホストアクセスフィールドを取ります。

| フィールド | 許可する対象 |
|---|---|
| `capabilities` | 名前付きバンドル — 後述 |
| `allow` | ディレクトリ、読み書き |
| `read` | ディレクトリ、読み取り専用 |
| `allow_file` | 単一ファイル、読み書き |
| `read_file` | 単一ファイル、読み取り専用 |
| `allow_env` | 環境変数名 |

`PATH` / `HOME` / `TERM` / `LANG` / `LC_ALL` / `USER` と `/dev/null` は、
組み込みのベースラインとして常に許可されます。

`NONO_*` はどの `allow_env` でも拒否されます — サンドボックス自体を再設定できて
しまう変数だからです。

保護対象プレフィックス (`~/.ssh`, `~/.aws`, `~/.docker`, `~/.gnupg`,
`~/.config/gh`, `~/.kube`) を生の `allow` / `read` で指定すると拒否されます。
対応する capability を使ってください。

### capability

ディレクトリ・ファイル・環境変数・ネットワークドメイン、そして認証情報系バンドルでは
対応する Claude の権限 deny ルールにまで展開される、名前付きバンドルです。

| capability | 許可する対象 | シェルサンドボックスに追加されるドメイン |
|---|---|---|
| `go` | Go ランタイムグループ | `proxy.golang.org`, `sum.golang.org` |
| `python` | Python ランタイムグループ | `pypi.org`, `files.pythonhosted.org` |
| `node` | Node ランタイムグループ | `registry.npmjs.org` |
| `rust` | Rust ランタイムグループ | `crates.io`, `index.crates.io`, `static.crates.io` |
| `dart` | `~/.pub-cache`, `~/.dart` (読み書き)、`PUB_CACHE` / `PUB_HOSTED_URL` 環境変数 | `pub.dev`, `storage.googleapis.com` |
| `flutter` | `~/.config/flutter` (読み書き)、`~/.flutter`, `~/.flutter_tool_state`、`FLUTTER_ROOT` / `FLUTTER_STORAGE_BASE_URL` 環境変数 | `storage.googleapis.com` |
| `docker` | `~/.docker`, `~/.orbstack` (読み取り専用) | `auth.docker.io`, `index.docker.io`, `registry-1.docker.io`, `production.cloudflare.docker.com` |
| `ssh` | `~/.ssh` (読み取り専用)、`~/.ssh/known_hosts` (読み書き) | — |
| `mise` | `~/.local/share/mise`, `~/.config/mise` (読み取り専用)、`~/.local/share/mise/http-tarballs` (読み書き)、`MISE*` 環境変数 | `mise.jdx.dev`, `mise-versions.jdx.dev` |
| `bashrc` | `~/.bashrc`, `/etc/bashrc`, `/etc/bash.bashrc` (読み取り専用) | — |

各ツールチェーンが自分のレジストリを持ち込むため、Go モジュールプロキシや PyPI を
設定側で書き直す必要はありません。

`flutter` は Dart の**差分**であって上位集合ではありません。Flutter プロジェクトは
`["dart", "flutter"]` の両方を宣言します。どちらも SDK のチェックアウト自体は許可
しません — flutter は自分の `bin/cache` に書き込むため SDK は書き込み可能である必要
があり、かつ万人に正しい固定パスが存在しないためです。`mise` 管理なら上の書き込み可能
なターボール領域が既にカバーしており、git チェックアウトなら設置場所を生の `allow`
で渡します。`dart` も `~/.dart-tool` は含みません。private な hosted リポジトリの
認証情報が置かれる場所だからです。

> **`docker` と `ssh` はホストの認証情報を露出します。** とはいえ扱いは他の
> capability と同じで、宣言した側にだけ適用されます。サンドボックス内のコマンドが
> 本当に鍵を必要とするのでない限り、`[sandbox.shared]` ではなく `[sandbox.agent]`
> に書いてください。シェルプロファイル側に認証情報パスが渡ってしまう設定では、
> `agent-sandbox debug` が警告します。

### コマンドルーティング

```toml
[sandbox.agent]
allow_commands = ["go *", "mise use *", "mise install *"]
drop_commands = [
  { pattern = "git *" },
  { pattern = "gh *", message = "gh is disabled in this sandbox. Use the GitHub MCP server's tools instead." },
]
```

`drop_commands` の各エントリは `{ pattern, message }` テーブルです。`message` は
任意で、省略すると既定の拒否メッセージが表示されます。

パターンに否定はなく、allow が先に評価されます。そのため allow で許可したものの
一部だけを切り出して除外することはできません。`"mise use *"` は `mise use -g` も
含みます。これを制限したい場合はプロファイル側に任せます — `mise` capability は
`~/.config/mise` を読み取り専用で許可するので、グローバル書き込みはリストではなく
サンドボックスによって拒否されます。

### ネットワーク

サンドボックス内のコマンドは nono の `developer` ネットワークプロファイル
(LLM API、パッケージレジストリ、GitHub、sigstore、ドキュメント) に加えて、
宣言された capability が持ち込むドメインの下で動きます。

```toml
[sandbox.shell]
allow_domains = ["internal.example.com"]   # どちらもカバーしないものだけを書く
```

ドメインは「広げるネットワークを持っている側」にのみ適用されます — つまり
シェルサンドボックスだけです。起動されるエージェントは nono のベースプロファイルの
ネットワークをそのまま使うため、`[sandbox.agent]` に宣言した capability は
ドメインを何も追加しません。

### ユーザースコープ設定

任意の `~/.config/agent-sandbox/config.toml` がプロジェクト設定と合成されます。

- **スカラー値**: プロジェクトファイルが優先
- **リスト**: 両者の重複排除された和集合 (ユーザー側のエントリが先)

つまりプロジェクトファイルはリストに *追加* できますが、ユーザースコープ設定が
持ち込むものを *削除* することはできません。書いた覚えのない許可が
`agent-sandbox ai config-check` に現れたら、出どころはここです。

## 環境変数 (`--env`)

`--env` は、Claude を起動する / コマンドを実行する前に、ファイルから環境変数を
プロセスに読み込みます。繰り返し指定でき、スキーム付きの参照を取ります。現時点で
存在するのは `file:` だけです。

```bash
agent-sandbox claude --env file:.env -- --model opus
agent-sandbox exec --env file:.env -- go test ./...
```

形式は dotenv の最小サブセットです: 1 行 1 つの `KEY=VALUE`、`#` コメントと
空行は無視、先頭の `export ` は除去、前後のクォートは削除。**変数展開はありません。**
値はホストの同名変数を **上書き** します。複数ファイルを指定した場合は後のものが
勝ちます。

`agent-sandbox claude` では、読み込んだキーが `[sandbox.agent].allow_env` に
追加されます — つまり `--env` は起動されるエージェントにのみ許可を与えます。
サンドボックス内のコマンドに変数を渡すかどうかは、明示的な設定編集のままです。

## GitHub MCP

組み込みの GitHub MCP サーバーは、`GITHUB_MCP_TOKEN` が空でないときに有効になります。
空の場合はそもそも設定されません。値は `GITHUB_PERSONAL_ACCESS_TOKEN` として MCP
サーバーに渡されます。

```bash
agent-sandbox claude --env file:.secrets.env -- --model opus
# .secrets.env の中身: GITHUB_MCP_TOKEN=ghp_...
```

`agent-sandbox debug` は、トークンを伏字にした MCP 設定を表示します。

## safe ラッパー

`agent-sandbox safe <tool> ...` は呼び出しを検証してから、そのまま素通しで実行します。
拒否した場合は何も実行せず exit 1 になります。

`agent-sandbox safe *` は常にホスト許可なので、素のツールを drop してラッパー経由
でのみ触らせる、というのが定番の使い方です。

```toml
[sandbox.agent]
drop_commands = [{ pattern = "git *" }]   # git はすべて `safe git` を通す
```

### `safe git`

```bash
agent-sandbox safe git push --force-with-lease
```

拒否される呼び出し:

| ルール | 拒否対象 |
|---|---|
| force-push | `push --force` / `-f`、`--delete` / `-d`、`--mirror`、`--prune`、`:` / `+` 付き refspec。`--force-with-lease` と `--force-if-includes` は許可 — ただし裸の `--force` が同時にある場合は拒否 |
| hard-reset | `reset --hard` |
| clean-force | `clean -f` / `--force` |
| branch-force-delete | `branch -D`、または `-d` と `--force` の併用 |
| filter-history | `filter-branch`, `filter-repo` |
| update-ref-delete | `update-ref -d` |
| reflog-expire | `reflog expire` |
| gc-prune | `gc --prune=now` / `--prune=all` |
| bypass-hooks | `--no-verify`、`--no-gpg-sign`、`commit -n`、`-c` / `--config-env` による `core.hooksPath` 設定や `commit.gpgsign` の無効化 |
| alias-injection | `-c alias.*=...` |
| config-exec-injection | `-c` による実行可能な設定キーの注入 (`core.sshCommand`, `core.pager`, `core.editor`, `credential.helper`, `gpg.program`, `diff.external` など) |
| stash-destroy | `stash drop`, `stash clear` |
| remote-tamper | `remote remove` / `rm` / `set-url` |
| tag-delete | `tag -d` |
| discard-changes | `checkout -- <path>` / `checkout .`、およびワーキングツリーを対象にした `restore` |
| config-write | 読み取り (`--get*`, `--list`, `-l`) 以外の `git config` |

### `safe docker-compose`

```bash
agent-sandbox safe docker-compose up -d
```

`docker compose config` でプロジェクトを解決し、次のいずれかに該当する場合は
拒否します。

- `bind` マウントの解決先がカレントワーキングディレクトリの外にある
- `bind` マウントが Docker ソケット (`docker.sock`) を指している
- サービスが `privileged: true` / `network_mode: host` / `pid: host` /
  `ipc: host` / `userns_mode: host` を設定している
- サービスがホストの `devices` を公開している
- `cap_add` に危険な capability が含まれる (`ALL`, `SYS_ADMIN`, `SYS_PTRACE`,
  `SYS_MODULE`, `SYS_RAWIO`, `SYS_BOOT`, `SYS_TIME`, `NET_ADMIN`, `NET_RAW`,
  `DAC_READ_SEARCH`, `DAC_OVERRIDE`, `MKNOD`)
- `security_opt` が拘束を無効化している (`*:unconfined`, `label:disable`)
- サブコマンドが `run` または `exec`
- 先頭のグローバルフラグを分類できない (フェイルクローズ)

名前付きボリュームと `tmpfs` マウントは許可されます。それ以外のサブコマンド
(`up`, `build`, `down`, `ps`, `logs` など) は素通しです。判定ルールは固定の
組み込みです。

## 開発

```bash
mise install          # Go と lefthook
go test ./...         # ユニットテスト + 統合テスト
go build ./...
```

E2E スイートは `tests/e2e` (Go/Ginkgo) と `e2e` (Python/pytest、MCP stdio) に
あります。

コミットは [Conventional Commits](https://www.conventionalcommits.org/) に従います。
`lefthook` が `commit-msg` でタイトルを検証します。

## ライセンス

[MIT](LICENSE) © Yuya Nagai
