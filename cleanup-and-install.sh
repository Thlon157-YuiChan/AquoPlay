#!/usr/bin/env bash
set -euo pipefail

echo "=== ユーザーローカルの不要ツールを削除 ==="

# 旧バイナリ
rm -f ~/.local/bin/dlna-player

# uv 環境設定 (不要)
rm -f ~/.local/bin/env ~/.local/bin/env.fish

# FFmpeg static build シンボリックリンク
rm -f ~/.local/bin/ffmpeg ~/.local/bin/ffplay ~/.local/bin/ffprobe

# FFmpeg static build 本体
rm -rf ~/.local/opt/ffmpeg-master-latest-linux64-gpl

# Go toolchain (apt で golang-go をインストール)
rm -rf ~/.local/go

# pydtcpip uv tool (pip で再インストール)
rm -rf ~/.local/share/uv/tools/pydtcpip

# uv キャッシュ (任意)
# rm -rf ~/.cache/uv

echo "=== apt で必要なツールをインストール ==="
sudo apt-get update
sudo apt-get install -y \
    ffmpeg \
    mpv \
    golang-go \
    python3 \
    python3-pip \
    python3-venv

echo "=== pydtcpip を pip でインストール ==="
pip3 install --user --break-system-packages pydtcpip

echo "=== aquoplay をビルド ==="
cd ~/dev/aquoplay
export PATH="/usr/bin:$PATH"
go build -o aquoplay .
cp aquoplay ~/.local/bin/aquoplay
chmod +x ~/.local/bin/aquoplay

echo "=== 確認 ==="
which ffmpeg ffplay mpv go python3 pydtcpip aquoplay
ffmpeg -version | head -1
mpv --version | head -1
go version
python3 --version
pydtcpip --help 2>&1 | head -1 || true
aquoplay --list --server http://127.0.0.1:1/ 2>&1 | head -1 || true

echo "=== 完了 ==="
