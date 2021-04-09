#!/bin/sh

echo "Install  gitlab runner"
curl -LJO "https://gitlab-runner-downloads.s3.amazonaws.com/latest/deb/gitlab-runner_amd64.deb"
sudo dpkg -i gitlab-runner_amd64.deb
sudo usermod -a -G sudo gitlab-runner
#sudo visudo
#gitlab-runner ALL=(ALL) NOPASSWD: ALL

echo "Install Docker"
sudo apt install apt-transport-https  ca-certificates curl  gnupg-agent software-properties-common -y
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo apt-key add -
sudo apt-key fingerprint 0EBFCD88
sudo add-apt-repository "deb [arch=amd64] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable"
sudo apt update -y
sudo apt install docker-ce docker-ce-cli containerd.io -y
#privileged = true
#volumes = ["/cache", "/dev:/dev"] - for docker ruuners in /etc/gitlab-runner/config.toml


echo "Install  Golang v 1.14"
curl -L https://golang.org/dl/go1.14.14.linux-amd64.tar.gz --output /tmp/go.tar.gz
sudo tar -C /usr/local -xzf /tmp/go.tar.gz
echo  PATH=$PATH:/usr/local/go/bin >> $HOME/.profile
mkdir -p $HOME/go/src

echo "Install  CNI"
git clone https://github.com/containernetworking/plugins.git
cd  plugins
./build_linux.sh
sudo mkdir /opt/cni
sudo mv ./bin/ /opt/cni/

echo "Install SM dependencies"
sudo apt install quota dnsmasq runc -y

echo "Install tests dependencies"
sudo apt install libsystemd-dev dbus-x11 make iperf libssl-dev rabbitmq-server -y
sudo systemctl stop rabbitmq-server
sudo systemctl disable rabbitmq-server

echo "Install python3.7 and libs"
sudo apt install python3-pip  -y
sudo -H pip3 install pyftpdlib
sudo apt install python3.7

echo "Install Whitesource"
sudo apt install default-jre -y
sudo curl -L https://unified-agent.s3.amazonaws.com/wss-unified-agent.jar --output /usr/bin/wss-unified-agent.jar