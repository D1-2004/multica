#!/bin/bash

stop_app() {
  pkill -f "/target/${APP_NAME}/bin/server" || true
  pkill -f "apps/web/server.js" || true
  return 0
}
