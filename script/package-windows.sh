#!/usr/bin/env bash
# ferryman.exe を Release アセットとして建てる。
#
#     script/package-windows.sh [version]
#
# version は tag と同じ形式（v1.2.3 / v1.2.3-rc.1）。省略時は v0.0.0-dev。
# 成果物は dist/ferryman_<version>_windows_amd64.exe。
#
# Release ワークフロー（.github/workflows/release.yml）の Windows ジョブはこれを
# 呼ぶだけにしてある。手元で配布物と同じものを作って確かめられるようにするため。
set -euox pipefail

# ツール版を CI と手元で揃える
GO_WINRES_VERSION=v0.3.3

version="${1:-v0.0.0-dev}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}"

if [ "$(go env GOOS)" != "windows" ]; then
  echo "package-windows.sh: Windows でのみ実行できる (GOOS=$(go env GOOS))" >&2
  exit 1
fi

arch="$(go env GOARCH)"
out="${root}/dist/ferryman_${version}_windows_${arch}.exe"
syso="${root}/cmd/ferryman/rsrc_windows_${arch}.syso"

mkdir -p "${root}/dist"

# Explorer やタスクバーのピン留めが読むのは PE のリソースなので、アイコンは
# go build の前に .syso として main パッケージへ置く。go-winres は
# cmd/ferryman/Icon.png から各サイズを起こして 1 ファイルに詰めてくれる。
# --out は cwd 起点なので cmd/ferryman に移ってから呼ぶ。マニフェストは付けない
# （GLFW が DPI 対応を実行時に設定するので要らず、変わるのはアイコンだけになる）
rm -f "${syso}"
trap 'rm -f "${syso}"' EXIT
(cd "${root}/cmd/ferryman" && go run "github.com/tc-hib/go-winres@${GO_WINRES_VERSION}" simply \
  --icon Icon.png --arch "${arch}" --manifest none)

# -H windowsgui: これがないと GUI の脇にコンソール窓が開く
# -v はコンパイル中のパッケージ名を流す。cgo で glfw を建てる間は無出力が長く、
# ログ上で停止と区別が付かないため
go build -v -ldflags "-H windowsgui -X main.version=${version}" -o "${out}" ./cmd/ferryman

echo "packaged ${out}"
