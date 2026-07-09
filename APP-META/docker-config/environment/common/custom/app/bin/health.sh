#!/bin/bash

health_check() {
  curl -fsS "http://127.0.0.1:${AONE_HEALTH_PORT:-6001}/check.node" >/dev/null
  curl -fsS "http://127.0.0.1:${BACKEND_PORT:-8080}/healthz" >/dev/null
  curl -fsS "http://127.0.0.1:${FRONTEND_PORT:-3000}/" >/dev/null
}
