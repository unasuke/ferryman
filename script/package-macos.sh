#!/usr/bin/env bash
# Ferryman.app を組み立てて Release アセットの zip にする。
#
#     script/package-macos.sh [version]
#
# version は tag と同じ形式（v1.2.3 / v1.2.3-rc.1）。省略時は v0.0.0-dev。
# 成果物は dist/ferryman_<version>_darwin_<arch>.app.zip、
# 中間物（素のバイナリと Ferryman.app）は build/macos/ に残る。
#
# Release ワークフロー（.github/workflows/release.yml）の macOS ジョブはこれを
# 呼ぶだけにしてある。手元で配布物と同じものを作って確かめられるようにするため。
set -euox pipefail

# ツール版を CI と手元で揃える
FYNE_TOOLS_VERSION=v1.7.2

version="${1:-v0.0.0-dev}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}"

if [ "$(go env GOOS)" != "darwin" ]; then
  echo "package-macos.sh: macOS でのみ実行できる (GOOS=$(go env GOOS))" >&2
  exit 1
fi

arch="$(go env GOARCH)"
work="${root}/build/macos"
out="${root}/dist/ferryman_${version}_darwin_${arch}.app.zip"

rm -rf "${work}"
mkdir -p "${work}" "${root}/dist"

# バンドル内の実行ファイル名がそのまま CFBundleExecutable になるので、
# 版番号を含まない素の "ferryman" として置く。
# -v はコンパイル中のパッケージ名を流す。cgo で glfw を建てる間は無出力が長く、
# ログ上で停止と区別が付かないため
go build -v -ldflags "-X main.version=${version}" -o "${work}/ferryman" ./cmd/ferryman

# PATH 上の fyne ではなく固定版を入れて使う（アイコン加工や Info.plist の中身が
# ツール版に依存するため）。module cache に載っていればリンクし直すだけで済む
go install "fyne.io/tools/cmd/fyne@${FYNE_TOOLS_VERSION}"
gobin="$(go env GOBIN)"
[ -n "${gobin}" ] || gobin="$(go env GOPATH)/bin"

# CFBundleShortVersionString は x.y.z のみ受け付ける。prerelease 部分は落とす
app_version="${version#v}"
app_version="${app_version%%-*}"

# .app は cwd に出る。--src と --executable は cwd を --src へ移した後に
# 解決されるので絶対パスで渡す。アイコン元画像は cmd/ferryman/Icon.png で、
# macOS 用の角丸と余白は fyne package が付ける
cd "${work}"
"${gobin}/fyne" package --target darwin --src "${root}/cmd/ferryman" \
  --executable "${work}/ferryman" \
  --name Ferryman --app-id com.unasuke.ferryman \
  --app-version "${app_version}"

# Developer ID を持たないので ad-hoc 署名。notarization されないため Gatekeeper は
# 通らない（README に隔離属性の外し方を書いてある）が、バンドル全体を封緘し直すことで
# Info.plist ごと署名され、arm64 の実行に必要な署名も満たす
codesign --force --sign - --identifier com.unasuke.ferryman Ferryman.app
codesign --verify --strict Ferryman.app

# ditto: 実行権限とシンボリックリンクを保つ zip（zip(1) では壊れる）
rm -f "${out}"
ditto -c -k --sequesterRsrc --keepParent Ferryman.app "${out}"

echo "packaged ${out}"
