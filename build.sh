#!/usr/bin/env bash

version=$(git describe --tags --always --dirty)

crosscompile () {
    if [ "$1" == "windows" ]; then
        name='sptenc.exe'
    else
        name='sptenc'
    fi
    # -trimpath: no local paths (home directory, user name) within the released binaries
    GOOS="$1" GOARCH="$2" go build -trimpath -ldflags="-s" -o "$name" ./cmd/sptenc
    zip -9 "sptenc_${version}_${1}_${2}.zip" "$name"
    rm "$name"
}

crosscompile 'windows' 'amd64'
crosscompile 'windows' 'arm64'
crosscompile 'linux' 'amd64'
crosscompile 'linux' 'arm64'
crosscompile 'darwin' 'arm64'
