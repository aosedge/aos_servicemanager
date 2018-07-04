#!/bin/bash

if [[ $EUID -ne 0 ]]; then
	echo "This script must be run as root" 1>&2
	exit 1
fi

echo Stopping aos_servicemanager...
systemctl stop aos_servicemanager
if [ $? != 0 ]; then
	echo "Can't stop aos_servicemanager" 1>&2
	exit 1
fi
echo Stopped

for f in data/services/* ; do
	if [ -d "$f" ]; then
		r=$(basename $(find $f -path '*.service'))
		if [ ! -z "$r" ]; then
			echo Stopping $r...
			systemctl stop $r
			echo Stopped

			echo Disabling $r...
			systemctl disable $r
			echo Disabled
		fi
	fi
done

echo Removing services folder...
rm data/services -rf
echo Removed

echo Removing database...
rm data/services.db
echo Removed

echo
echo =========================================================================
echo WARNING: aos_servicemanager is stopped. To start it use following command:
echo '	sudo systemctl start aos_servicemanager'
echo or:
echo '	sudo reboot'
echo ':)'
echo =========================================================================
