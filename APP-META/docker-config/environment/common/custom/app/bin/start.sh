#!/bin/bash

start_app() {
  local app_root="/home/admin/${APP_NAME}"
  echo "start ${APP_NAME} from ${app_root}"
  exec bash "${app_root}/target/${APP_NAME}/src/main.sh"
}
