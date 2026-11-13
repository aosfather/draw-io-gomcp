#!/usr/bin/env bash
#
# draw-io-gomcp multi-platform release builder
# Produces stripped static binaries for darwin/linux/windows, tar.gz or zip
# packaged, ready to upload as GitHub release assets under dist/.
#
# Usage:
#   bash build.sh            # build all platforms
#   bash build.sh linux      # build only linux/*
#   bash build.sh darwin     # build only darwin/*
#   bash build.sh clean      # remove dist/ and exit
#
set -euo pipefail

APP="draw-io-gomcp"
PKG="./cmd/server"

# 从 git tag 取版本，fallback 到 dev-<commit>
if TAG=$(git describe --tags --abbrev=0 2>/dev/null); then
  VERSION="${TAG#v}"
else
  COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
  VERSION="dev-${COMMIT}"
fi

OUTDIR="dist"
mkdir -p "$OUTDIR"

# release build flags: 零依赖、裁掉 debug/symbol、去掉 build path
LDFLAGS_STR="-s -w"

# 目标平台: (GOOS GOARCH archive-ext)
TARGETS=(
  "darwin  arm64  tar.gz"
  "darwin  amd64  tar.gz"
  "linux   amd64  tar.gz"
  "linux   arm64  tar.gz"
  "windows amd64  zip"
)

# 命令行过滤 (bash build.sh linux)
FILTER="${1:-all}"
shift || true

build_one() {
  local goos="$1" goarch="$2" ext="$3"
  local bin="${APP}"
  if [ "$goos" = "windows" ]; then bin="${APP}.exe"; fi

  local pkg="${OUTDIR}/${APP}-${VERSION}-${goos}-${goarch}"
  mkdir -p "$pkg"

  echo "==> ${goos}/${goarch}  ->  ${pkg}/${bin}"
  (
    CGO_ENABLED=0 \
    GOOS="$goos"   \
    GOARCH="$goarch" \
    go build -trimpath "-ldflags=${LDFLAGS_STR}" -o "${pkg}/${bin}" ${PKG}
  )

  # 打包成 archive, 目录根就是 binary + README + LICENSE + NOTICE
  cp README.md "${pkg}/" 2>/dev/null || true
  cp LICENSE   "${pkg}/" 2>/dev/null || true
  cp NOTICE    "${pkg}/" 2>/dev/null || true

  local archive="${OUTDIR}/${APP}-${VERSION}-${goos}-${goarch}.${ext}"
  rm -f "$archive"
  case "$ext" in
    tar.gz) tar -czf "$archive" -C "$OUTDIR" "$(basename "$pkg")" ;;
    zip)    zip -qr "$archive" "$pkg" ;;
  esac
  rm -rf "$pkg"
  echo "    -> ${archive} ($(du -h "$archive" | cut -f1))"
}

# ---- skills 打包 (普通 skills + .dsh 兼容层) ----
SKILLS_SRC="skills"
DSH_SKILLS_SRC=".dsh/skills"

build_skills() {
  local has_skills=false
  [ -d "$SKILLS_SRC" ] && has_skills=true
  [ -d "$DSH_SKILLS_SRC" ] && has_skills=true

  if [ "$has_skills" = false ]; then
    echo "==> skills/ 和 .dsh/skills/ 都不存在, 跳过 skill 打包"
    return 0
  fi

  echo
  echo "==> 打包 skills (含 .dsh 兼容层)"

  local skill_archive="${OUTDIR}/${APP}-skills-${VERSION}.tar.gz"
  rm -f "$skill_archive"

  # 打包 skills + .dsh/skills, 排除 macOS/编辑器垃圾文件
  # 注意: tar 参数顺序决定了归档内的相对路径
  local tar_args=(--exclude='.DS_Store' --exclude='.git' --exclude='*.swp' --exclude='__pycache__')
  if [ -d "$SKILLS_SRC" ]; then
    tar_args+=("$SKILLS_SRC")
  fi
  if [ -d "$DSH_SKILLS_SRC" ]; then
    tar_args+=("$DSH_SKILLS_SRC")
  fi
  tar -czf "$skill_archive" "${tar_args[@]}"

  echo "    -> ${skill_archive} ($(du -h "$skill_archive" | cut -f1))"
}

case "$FILTER" in
  clean) rm -rf "$OUTDIR"; echo "cleaned dist/"; exit 0 ;;
  darwin)  PLAT_FILTER="darwin" ;;
  linux)   PLAT_FILTER="linux" ;;
  windows) PLAT_FILTER="windows" ;;
  all)     PLAT_FILTER="" ;;
  *) echo "unknown filter: $FILTER (darwin|linux|windows|all|clean)" >&2; exit 1 ;;
esac

echo "== ${APP} ${VERSION} =="
echo "ldflags: ${LDFLAGS_STR}"
echo "output : ${OUTDIR}/"
echo

for t in "${TARGETS[@]}"; do
  set -- $t
  goos="$1" goarch="$2" ext="$3"
  if [ -n "$PLAT_FILTER" ] && [ "$goos" != "$PLAT_FILTER" ]; then continue; fi
  build_one "$goos" "$goarch" "$ext"
done

build_skills

echo
echo "== done =="
ls -lh "${OUTDIR}/"
echo
echo "Next: upload dist/* to https://github.com/aosfather/draw-io-gomcp/releases"
