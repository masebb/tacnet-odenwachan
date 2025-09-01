# tacnet-odenwakun Discord Bot (Go minimal)

MikoPBX の SIP 端末・プロバイダのオンライン/オフライン変化をポーリングし、Discord に通知します。

## 必要条件

- Go 1.20+
- Discord Bot Token（環境変数 `DISCORD_TOKEN`）
- Discord の投稿先チャンネル ID（環境変数 `DISCORD_CHANNEL_ID`）
- MikoPBX のベース URL（環境変数 `MIKOPBX_BASE_URL`）
- 必要に応じてログイン情報（環境変数 `MIKOPBX_LOGIN` / `MIKOPBX_PASSWORD`）

## 使い方

1. ビルド

```zsh
go build -o bin/bot
./bin/bot
```

## 構成

- `src/main.go` — エントリポイント。環境変数で設定を受け取り監視を起動（--debug で詳細ログ）。

## トラブルシュート

- Discord 接続に失敗する → `--discord-token`/`--discord-channel` を確認。
- 4014（Disallowed intent） → 通知用途のみなので現行は IntentsGuilds のみ。基本発生しません。
- MikoPBX の認証エラー → `--mikopbx-login`/`--mikopbx-password` を確認。

## 実行例

```zsh
export DISCORD_TOKEN="xxxxx"
export DISCORD_CHANNEL_ID="123456789012345678"
export MIKOPBX_BASE_URL="http://172.16.156.223"
export MIKOPBX_LOGIN="admin"
export MIKOPBX_PASSWORD="adminpassword"
export POLL_INTERVAL_SEC=30

./build/tacnet-odenwakun --debug
```
