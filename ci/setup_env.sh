#!/bin/sh

echo "Install  Golang v 1.14"

curl https://dl.google.com/go/go1.14.4.linux-amd64.tar.gz --output /tmp/go.tar.gz
sudo tar -C /usr/local -xzf /tmp/go.tar.gz
echo  PATH=$PATH:/usr/local/go/bin >> $HOME/.profile

mkdir -p $HOME/go/src

echo "Install SM dependencies"
sudo apt update -y
sudo apt install libsystemd-dev dbus-x11 make iperf quota runc -y

echo "Install wondershaper"
cd /tmp
git clone  https://github.com/magnific0/wondershaper.git
cd wondershaper
sudo make install

echo "Install tests dependencies"
sudo apt install libssl-dev -y
sudo apt install rabbitmq-server -y
sudo systemctl disable rabbitmq-server

sudo apt install python3-pip -y
sudo -H pip3 install pyftpdlib

echo "Install gitlab runner dependencies"
curl -L https://packages.gitlab.com/install/repositories/runner/gitlab-runner/script.deb.sh | sudo bash
sudo apt-get install gitlab-runner
sudo usermod -a -G sudo gitlab-runner
