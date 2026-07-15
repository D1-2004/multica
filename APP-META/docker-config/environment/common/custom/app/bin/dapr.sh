#!/bin/bash

dapr_before_start() {
  echo "INFO: [dapr] syncing custom configuration"

  local source_dir="${APP_HOME}/conf/dapr-custom"
  local shared_dir="${APP_HOME}/dapr"

  if [[ ! -d "$source_dir" ]]; then
    echo "ERROR: [dapr] custom configuration not found: ${source_dir}"
    return 1
  fi
  mkdir -p "$shared_dir" || {
    echo "ERROR: [dapr] cannot create shared directory: ${shared_dir}"
    return 1
  }
  cp -r "${source_dir}/." "$shared_dir/" || {
    echo "ERROR: [dapr] cannot copy custom configuration"
    return 1
  }
  touch "${shared_dir}/DONE"

  echo "INFO: [dapr] custom configuration ready"
}
