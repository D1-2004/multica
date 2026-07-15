#!/bin/bash

source "/home/admin/${APP_NAME}/bin/dapr.sh"

start_app() {
  local app_root="/home/admin/${APP_NAME}"
  dapr_before_start || return 1
  echo "start ${APP_NAME} from ${app_root}"
  bash "${app_root}/target/${APP_NAME}/src/main.sh"
}
