#!/bin/bash
# Wrapper to launch codex with proper PATH
export PATH="/Users/cliff/Library/Application Support/Herd/config/nvm/versions/node/v22.21.1/bin:$PATH"
exec codex "$@"
