# aquoplay

DTCP-IP 対応レコーダーに保存された録画を、ターミナル UI から選んで
mpv / ffplay でストリーミング再生する Go 製ラッパーです。
内部では [pydtcpip](https://pypi.org/project/pydtcpip/) を呼び出し、
DTCP 暗号化されたストリームを復号して再生します。
E-EMI 0x0 (暗号化なし) の録画も再生できます。

## 必要条件

- Python 3.13 以上と `pydtcpip` (`uv tool install pydtcpip` または `pip install pydtcpip`)
- `mpv` (推奨) または `ffplay` (libaribcaption 入りビルド推奨)
- DTCP-IP デバイス認証情報 `credentials.env`
- Go 1.23 以上 (ビルドする場合)

## セットアップ

### 1. pydtcpip

```bash
uv tool install pydtcpip
# または
pip install pydtcpip
```

### 2. プレイヤー

mpv (推奨):

```bash
# Debian/Ubuntu
sudo apt install mpv

# または FFmpeg-Builds から ffplay を取得
# https://github.com/FFmpeg-Builds/FFmpeg-Builds
```

### 3. credentials.env の取得

DTCP-IP デバイス認証情報 (`credentials.env`) をなんらかの方法で用意してください。
pydtcpip のドキュメントを参照してください。

生成された `credentials.env` を `~/.local/share/pydtcpip/` に配置してください。
pydtcpip は起動時に自動で読み込みます。

### 4. ビルド

```bash
git clone https://github.com/your-username/aquoplay.git
cd aquoplay
go build -o aquoplay .
cp aquoplay ~/.local/bin/
```

## 使い方

```bash
# レコーダーを自動探索
aquoplay

# サーバ URL を直接指定
aquoplay --server http://<recorder-ip>:<port>/

# 録画一覧だけ表示
aquoplay --list
aquoplay --list --server http://<recorder-ip>:<port>/
```

## キー操作

| キー | 動作 |
|------|------|
| ↑ / k | 上に移動 |
| ↓ / j | 下に移動 |
| P / Enter | フォルダを開く / 録画を再生 |
| Backspace | 親フォルダに戻る |
| r | 録画一覧を再読込 |
| Esc | レコーダー選択に戻る (複数台時) |
| q / Ctrl+C | 終了 |

再生中は mpv/ffplay のウィンドウで操作します。

## E/F プレフィックス

録画一覧の各行には E または F のプレフィックスが付きます。

- **E**: DTCP 暗号化された録画 (pydtcpip で復号)
- **F**: 平文 (非暗号化) の録画 (直接 HTTP ストリーミング)

## トラブルシュート

- **レコーダーが見つからない**: `--interface` で PC の LAN アドレスを指定するか、`--server` で URL を直接指定してください。
- **credentials が無い**: `~/.local/share/pydtcpip/credentials.env` の存在を確認してください。
- **再生が開始されない**: mpv/ffplay がインストールされているか確認してください。
- **一部の録画が再生できない**: E-EMI 0x0 の録画は pydtcpip のモンキーパッチで対応済みです。それでも再生できない場合はログ (`/tmp/dlna-py.log`, `/tmp/dlna-mpv.log`) を確認してください。

## ライセンス

MIT

## 謝辞

- [pydtcpip](https://pypi.org/project/pydtcpip/) — DTCP-IP / DLNA プロトコル実装
- [@markun_Dh](https://x.com/markun_Dh) — DTCP-IP 鍵についての研究
