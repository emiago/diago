#!/bin/bash

PWD=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
DEST=$(realpath $PWD/../diago-public)

echo $PWD

rsync -avR --progress --inplace --delete --dry-run \
    --exclude='.git' \
    --exclude=$PWD/.git \
    --exclude=$PWD/playback_url.go --exclude=$PWD/playback_url_test.go \
    --exclude=$PWD/dialog_session_server_webrtc.go \
    --exclude=$PWD/recording.go --exclude=$PWD/recording_test.go \
    --exclude=$PWD/examples/webrtc \
    --exclude=$PWD/diagomod \
    --exclude='*.md' \
    --exclude='*.sh' \
    --exclude=$PWD/cmd \
    --exclude='go.work*' \
    $PWD \
	$DEST
