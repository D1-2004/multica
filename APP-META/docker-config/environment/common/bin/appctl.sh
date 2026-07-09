#!/bin/bash

cd $(dirname $0)/..

APP_HOME=$(pwd)
ACTION=$1
source ${APP_HOME}/bin/setenv.sh
source ${APP_HOME}/bin/hook.sh

if [ -z "$APP_NAME" ]; then
    APP_NAME=$(basename "${APP_HOME}")
fi
# uncompress to this dir
APP_ROOT=${APP_HOME}/target/${APP_NAME}
# TODO: the binary's name, used to start service & check process
TARGET=${APP_NAME}

# usage
usage() {
    echo "Usage: $PROG_NAME {offline|online|stop|start|pubstart|restart|status}"
    exit 1;
}

# check if it is runed by the admin user 
check_admin() {
    user=`id -nu`
    if [ ${user} != 'admin' ]
    then
        echo "ERROR: Stop! Only admin can run this script!"
        exit 3
    fi
}

offline() {
    echo
    echo "INFO: function offline: begin ..."
    rm -f ${STATUSROOT_HOME}/status.taobao
    rm -f ${STATUSROOT_HOME}/checkpreload.htm
    sleep 1
    echo "INFO: function offline: end"
}

online() {
    echo
    echo "INFO: function online: begin ..."
    if [[ ! -d ${STATUSROOT_HOME} ]]; then
        mkdir -p ${STATUSROOT_HOME}
    fi
    touch ${STATUSROOT_HOME}/status.taobao || exit 1
    echo 'success' > ${STATUSROOT_HOME}/checkpreload.htm || exit 1
    sleep 1
    echo "INFO: function online: end"
}

_proc_exists()
{
    if [ "$1" == "" ]; then
        return 1
    fi
    # count=`pgrep -f "$1" | wc -l`
    # -f matches full command line
    count=`pgrep "$1" | wc -l`
    if [ $count == 0 ]; then
        return 1
    else
        return 0
    fi
}

_proc_pids()
{
    if [ "$1" == "" ]; then
        return
    fi
    pids=`pgrep "$1"`
    # pids=`pgrep -f "$1"`
    echo $pids
}

_proc_state()
{
    if [ "$1" == "" ]; then
        return
    fi
    ps -ef | grep "$1" | grep -v 'grep'
    echo
}

_start_apps() {
    echo "start ${TARGET} service ..."
    mkdir -p ${APP_HOME}/logs
    sudo systemctl start $TARGET
    sudo systemctl status $TARGET
}

_stop_apps() {
    echo "stop ${TARGET} service ..."
    sudo systemctl stop $TARGET
    sudo systemctl status $TARGET
}

status() {
    # 1. check tengine state
    echo "tengine state:"
    _proc_state 'tengine'

    # 2. check app state
    echo "app state:"
    _proc_state ${TARGET}
}

stop() {
    echo 
    beforeStopApp
    echo "INFO: function stop: begin ..."
    echo "INFO: stop apps"
    _stop_apps
    if [ "${NGINX_SKIP}" -ne "1" ]; then
        echo "INFO: stop nginx"
        #/etc/init.d/tengine-live stop
        sh ${NGINXCTL} stop
    fi
    echo "INFO: function stop: end"
    afterStopApp
}

_init_paths(){
    # creat target folder, if there not exist target folder 
    echo "INFO: init paths ..."
    mkdir -p ${APP_HOME}/target/
}

_update_package(){
    echo "INFO: updata package..."
    rm -rf ${APP_HOME}/target/${APP_NAME}.bak
    if [[ -d ${APP_HOME}/target/${APP_NAME} ]]; then 
        mv ${APP_HOME}/target/${APP_NAME} ${APP_HOME}/target/${APP_NAME}.bak
    fi
    cd ${APP_HOME}/target
    tar -xzf ${APP_HOME}/target/${APP_NAME}.tgz || exit 1
    cd -
}

_setup_config()
{
    echo "INFO: setup config..."

    echo "install ${TARGET} service ..."
    sudo rm -f /etc/systemd/system/${TARGET}.service
    sudo cp /home/admin/app.service.tmpl /etc/systemd/system/${TARGET}.service || exit
    TARGET_PATH=${APP_ROOT}/_build/${TARGET} # TODO: _build can be changed when build.
    if [ ! -f $TARGET_PATH ];then
        TARGET_PATH=`find ${APP_HOME}/target/ -name ${TARGET} -type f`
    fi

    sudo sed -i "s|{TARGET}|${TARGET}|g;s|{TARGET_PATH}|${TARGET_PATH}|g" /etc/systemd/system/${TARGET}.service || exit

    # Reload systemd manager configuration
    sudo systemctl daemon-reload

    # TODO: env is set in Dockerfile
    # case "$env" in
    #     "testing")
    #         cp -f ${APP_ROOT}/testing.options.json ${APP_ROOT}/options.json
    #     ;;
    #     "staging")
    #         cp -f ${APP_ROOT}/staging.options.json ${APP_ROOT}/options.json
    #     ;;
    #     "production")
    #         cp -f ${APP_ROOT}/production.options.json ${APP_ROOT}/options.json
    #     ;;
    # esac
}

start() {
    echo
    beforeStartApp
    echo "INFO: function start: begin ..."
    # creat target folder, if there not exist target folder 
    _init_paths
    _update_package
    _setup_config
    # start tengine
    if [ "${NGINX_SKIP}" -ne "1" ]; then
        echo "INFO: start tengine"
        #/etc/init.d/tengine-live start || exit 1
        sh ${NGINXCTL} start
        sleep 1
    fi
    # start apps
    _start_apps
    echo "INFO: function start: end"
    afterStartApp
    echo
    echo "INFO: status: "
    status
}

# check if it is runed by the admin user 
check_admin

# check if the inputed arguments is correct
if [ $# -ne 1 ]; then
    usage
fi

case "$ACTION" in
    start)
        start
        online
        ;;
    stop)
        offline
        sleep 60
        stop
        ;;
    pubstart)
        stop
        start
        online
        ;;
    online)
        online
        ;;
    offline)
        offline
        ;;
    restart)
        offline
        sleep 60
        stop
        start
        sleep 10
        online
        ;;
    status)
        status
        ;;
    *)
        usage
        ;;
esac
