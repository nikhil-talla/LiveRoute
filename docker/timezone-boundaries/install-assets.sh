#!/bin/sh
set -eu

source_path=/opt/liveroute/share/timezone-boundaries
target_path=/export

/usr/local/bin/liveroute-verify-timezone-boundaries
mkdir -p "$target_path"
cp -R "$source_path"/. "$target_path"/
