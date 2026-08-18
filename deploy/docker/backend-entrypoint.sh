#!/usr/bin/env bash
#
# Starts the ads-for-gas service from the shared image.
#
# The chain and this service ship in one image and differ only by which
# entrypoint the Akash SDL invokes. That keeps it to one build, one digest and
# one thing to pin, at the cost of each pod carrying the other's payload.
#
# GAS_WALLET_MNEMONIC must be set or chain.init() refuses to start, which is
# deliberate: sending from a key nobody meant to use is worse than not starting.
set -euo pipefail

cd /app/backend
exec /opt/venv/bin/uvicorn main:app --host 0.0.0.0 --port "${PORT:-8000}"
