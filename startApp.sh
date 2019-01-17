#!/bin/bash
if [ -z $MFS_MASTER ]
then
	echo "not found MFS_MASTER"
	export MFS_MASTER="mfs-master.sit.ptcloud.t.home"
	echo MFS_MASTER=$MFS_MASTER
else
	echo MFS_MASTER=$MFS_MASTER
fi

mkdir -p /data
mfsmount  /data -H $MFS_MASTER
mfssettrashtime 10  /data

unset HTTP_PROXY
unset HTTPS_PROXY

mkdir -p /data/Sit/go-fluentd/buf/forward
./go-fluentd \
	--env=sit \
	--addr=0.0.0.0:22800 \
	--log-level=$LOG_LEVEL \
	--config-server=$CONFIG_SERVER_URL \
	--config-server-appname=$CONFIG_SERVER_APP \
	--config-server-profile=$CONFIG_SERVER_PROFILE \
	--config-server-label=$CONFIG_SERVER_LABEL \
	--config-server-key=$CONFIG_SERVER_KEY
