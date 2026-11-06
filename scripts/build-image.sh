#!/bin/sh
set -eu
# 必须使用与发布包相同的已验证静态资源；构建过程不上传或发布镜像。
ref=$(sed -n 's/^ref: "\([a-f0-9]*\)"/\1/p' build/frontend.lock.yaml)
commit=$(git rev-parse HEAD)
time=$(git show -s --format=%cI HEAD)
docker build --build-arg "VERSION=${MIHOMOHUB_VERSION:-0.3.0}" --build-arg "BACKEND_COMMIT=$commit" --build-arg "FRONTEND_COMMIT=$ref" --build-arg "BUILD_TIME=$time" -t "${MIHOMOHUB_IMAGE:-mihomo-hub:0.3.0}" .
