#!/usr/bin/env bash

version=$(git describe --tags --always --dirty)

crosscompile () {
    if [ "$1" == "windows" ]; then
        name='scenc.exe'
    else
        name='scenc'
    fi
    GOOS="$1" GOARCH="$2" go build -ldflags="-s -w -X 'main.Version=${version}'" -o "$name"
    zip -9 "scenc_${version}_${1}_${2}.zip" "$name"
    rm "$name"
}

crosscompile 'windows' 'amd64'
crosscompile 'linux' 'amd64'
