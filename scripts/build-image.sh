#!/bin/sh
set -eu
if [ "$#" -gt 0 ]; then
  python3 -c 'import hashlib,json,pathlib,re,sys; p=pathlib.Path(sys.argv[1]); m=json.loads((p/"manifest.json").read_text()); assert re.fullmatch("[0-9a-f]{40}",m["backend_commit"]) and re.fullmatch("[0-9a-f]{40}",m["frontend_commit"]); assert m["target"] in ("linux/amd64","linux/arm64"); assert hashlib.sha256((p/"mihomo-hub").read_bytes()).hexdigest()==m["binary_sha256"]; assert hashlib.sha256((p/"ca-certificates.crt").read_bytes()).hexdigest()==m["ca_bundle_sha256"]' "$1"
  # 发布目录已经过双仓库校验；从 manifest 读取版本，防止用工作区 HEAD 冒充二进制来源。
  release_dir=$1
  commit=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["backend_commit"])' "$release_dir/manifest.json")
  ref=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["frontend_commit"])' "$release_dir/manifest.json")
  version=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$release_dir/manifest.json")
  platform=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["target"])' "$release_dir/manifest.json")
  exec docker build --platform "$platform" -f Dockerfile.release --build-arg "BACKEND_COMMIT=$commit" --build-arg "FRONTEND_COMMIT=$ref" --build-arg "VERSION=$version" -t "${MIHOMOHUB_IMAGE:-mihomo-hub:$version}" "$release_dir"
fi
# 源码多阶段构建；构建过程不上传或发布镜像。
ref=$(sed -n 's/^ref: "\([a-f0-9]*\)"/\1/p' build/frontend.lock.yaml)
commit=$(git rev-parse HEAD)
time=$(git show -s --format=%cI HEAD)
docker build --build-arg "VERSION=${MIHOMOHUB_VERSION:-0.3.0}" --build-arg "BACKEND_COMMIT=$commit" --build-arg "FRONTEND_COMMIT=$ref" --build-arg "BUILD_TIME=$time" -t "${MIHOMOHUB_IMAGE:-mihomo-hub:0.3.0}" .
