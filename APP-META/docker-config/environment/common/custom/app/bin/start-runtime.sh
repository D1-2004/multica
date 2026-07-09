#!/bin/bash
set -euo pipefail

source "/home/admin/${APP_NAME}/bin/start.sh"
start_app "$@"
