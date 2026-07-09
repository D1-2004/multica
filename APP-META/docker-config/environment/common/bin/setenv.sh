# app
# set ${APP_NAME}, if empty $(basename "${APP_HOME}") will be used.
APP_NAME=
NGINX_HOME=/home/admin/cai

# os env
export LANG=zh_CN.UTF-8
export LD_LIBRARY_PATH=/opt/taobao/oracle/lib:/opt/taobao/lib:$LD_LIBRARY_PATH
export PATH=/home/admin/.rvm/bin:/home/tops/bin:$PATH
#export CPU_COUNT="$(grep -c 'cpu[0-9][0-9]*' /proc/stat)"
CPU_COUNT=$SIGMA_MAX_PROCESSORS_LIMIT
if [ ! -n "$CPU_COUNT" ];then
    CPU_COUNT=$(grep -c 'cpu[0-9][0-9]*' /proc/stat);
fi
export CPU_COUNT
ulimit -c unlimited

# 坑: 默认是1，也就是不启动nginx.....一定要改成0
# if set to "1", skip start nginx.
NGINX_SKIP=0

# set port for checking status.taobao file. Comment it if no need.
test "$NGINX_SKIP" = "0" && STATUS_PORT=80

# env check and calculate
#
if [ -z "$APP_NAME" ]; then
        APP_NAME=$(basename "${APP_HOME}")
fi
if [ -z "$NGINX_HOME" ]; then
        NGINX_HOME=/home/admin/cai
fi

STATUSROOT_HOME="${NGINX_HOME}/htdocs"
NGINXCTL=$NGINX_HOME/bin/nginxctl
APP_LOG=$APP_HOME/logs/${APP_NAME}.log
