#!/bin/sh
# Prepares a plain debian node, as a sprite is prepared: the fire has copied
# satelle in already; this installs the tools the node and the probe need.
set -eu
command -v satelle
satelle --version
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends git jq ca-certificates
