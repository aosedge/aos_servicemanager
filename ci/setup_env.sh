#!/bin/sh

echo "Install  Golang v 1.12"

curl https://dl.google.com/go/go1.12.16.linux-amd64.tar.gz --output /tmp/go.tar.gz
sudo tar -C /usr/local -xzf /tmp/go.tar.gz
echo  PATH=$PATH:/usr/local/go/bin >> $HOME/.profile
source $HOME/.profile

mkdir -p $HOME/go/src

sudo apt install libsystemd-dev -y
sudo apt install dbus-x11

echo "Install  netns"

export NETNS_SHA256="8a3a48183ed5182a0619b18f05ef42ba5c4c3e3e499a2e2cb33787bd7fbdaa5c"
# Download and check the sha256sum.
sudo curl -fSL "https://github.com/genuinetools/netns/releases/download/v0.5.3/netns-linux-amd64" -o "/usr/local/bin/netns" \
	&& echo "${NETNS_SHA256}  /usr/local/bin/netns" | sha256sum -c - \
	&& sudo chmod a+x "/usr/local/bin/netns"

cd /tmp
git clone  https://github.com/magnific0/wondershaper.git
cd wondershaper
sudo make install

sudo apt install iperf
