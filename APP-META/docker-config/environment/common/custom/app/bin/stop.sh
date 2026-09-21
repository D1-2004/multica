#!/bin/bash

stop_app() {
  # Stop the restart owner before signalling its child.
  pkill -TERM -f "/target/${APP_NAME}/src/frontend-supervisor[.]cjs" || true
  for attempt in $(seq 1 8); do
    pgrep -f "/target/${APP_NAME}/src/frontend-supervisor[.]cjs" >/dev/null || break
    sleep 1
  done
  pkill -KILL -f "/target/${APP_NAME}/src/frontend-supervisor[.]cjs" || true
  pkill -f "/target/${APP_NAME}/bin/server" || true
  pkill -f "apps/web/server.js" || true
  pkill -f "/home/admin/${APP_NAME}/run/health-server.js" || true
  return 0
}
