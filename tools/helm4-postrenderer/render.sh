#!/bin/sh
set -eu
exec python3 "${OPS_POST_RENDERER_SCRIPT:?Set the verified Harbor renderer path}"
