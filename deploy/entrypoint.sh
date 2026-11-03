#!/bin/sh
set -e

# В headful-режиме Chrome нужен X-сервер, поэтому поднимаем виртуальный.
if [ "${CHROME_HEADLESS:-true}" = "false" ] || [ "${CHROME_HEADLESS:-true}" = "0" ]; then
    exec xvfb-run -a --server-args="-screen 0 1920x1080x24" /usr/local/bin/bot "$@"
fi

exec /usr/local/bin/bot "$@"
