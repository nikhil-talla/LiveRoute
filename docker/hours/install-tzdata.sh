#!/bin/sh
set -eu

source_path=/opt/liveroute/share/tzdata
target_path=/export

/usr/local/bin/liveroute-verify-hours-assets
mkdir -p "$target_path"
cp -R "$source_path"/. "$target_path"/
