#!/bin/sh
set -eu
command -v satelle
satelle --version
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends git jq ca-certificates
