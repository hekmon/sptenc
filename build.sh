#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

# set by each crosscompile, and the same for all the binaries of a run: names the sums file
version=""

crosscompile () {
    local name
    if [ "$2" == "windows" ]; then
        name="$1.exe"
    else
        name="$1"
    fi
    echo "Compiling ${1} for ${2}/${3}..."
    # no cgo: static binaries, independent of the libc of the machine building them
    CGO_ENABLED=0 GOOS="$2" GOARCH="$3" go build -trimpath -ldflags="-s" -o "$name" "./cmd/${1}"
    # the version Go stamped, which --version reports (a tag, or a pseudo-version, "+dirty" for
    # uncommitted changes), rather than git describe, which only agrees with it on a clean tag
    version=$(go version -m "$name" | awk '$1 == "mod" { print $3 }')
    zip -9 "${1}_${version}_${2}_${3}.zip" "$name"
    rm "$name"
    echo
}

crosscompile 'sptenc' 'windows' 'amd64'
crosscompile 'sptenc' 'windows' 'arm64'
crosscompile 'sptenc' 'linux' 'amd64'
crosscompile 'sptenc' 'linux' 'arm64'
crosscompile 'sptenc' 'darwin' 'arm64'
