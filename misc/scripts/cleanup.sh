#!/bin/bash

print_usage()
{
	echo "Usage: `basename $0` <servicemanager> <workingdir>"
	echo "    <servicemanager> - systemd service manager service"
	echo "    <workingdir> - path to service manager working directory"
}

if [ "$1" == "-h" ]; then
	print_usage
	exit 0
fi

if [ "$#" -ne 2 ]; then
	print_usage
	exit 1
fi

if [[ $EUID -ne 0 ]]; then
	echo "This script must be run as root" 1>&2
	exit 1
fi

echo "Stopping $1..."
systemctl stop $1
if [ $? == 0 ]; then
	echo "Stopped"
fi

for f in $2/services/* ; do
	if [ -d "$f" ]; then
		r=$(basename $(find $f -path '*.service'))
		if [ ! -z "$r" ]; then
			echo "Stopping $r..."
			systemctl stop $r
			echo "Stopped"

			echo "Disabling $r..."
			systemctl disable $r
			echo "Disabled"
		fi
	fi
done

echo "Removing services folder..."
rm $2/services -r
if [ $? == 0 ]; then
	echo "Removed"
fi

echo Removing database...
rm $2/servicemanager.db
if [ $? == 0 ]; then
	echo "Removed"
fi

USERS=($(cut -d: -f1 /etc/passwd))

for i in "${USERS[@]}"
do
	if [[ $i == AOS_* ]]; then
		deluser $i
	fi
done

echo
echo "========================================================================="
echo "WARNING: $1 is stopped. To start it use following command:"
echo "	sudo systemctl start $1"
echo "or:"
echo "	sudo reboot"
echo ":)"
echo "========================================================================="
