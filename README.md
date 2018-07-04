# Build instruction:

```
go get github.com/cavaliercoder/grab
go get github.com/streadway/amqp

go get github.com/sirupsen/logrus github.com/mattn/go-sqlite3 github.com/opencontainers/runtime-spec/specs-go github.com/containerd/go-runc

for testing
go get gopkg.in/jarcoal/httpmock.v1

go build

```

# Configuration:

aos_servicemanager expects to have configuration file aos_servicemanager.cfg in current directory.
Or configuration file could be provide by command line parameter -c:

```
./aos_servicemanager -c aos_servicemanager.cfg
```

Example configuration file could be found in misc/config/aos_servicemanager.cfg

To increase log level use option -v:

```
./aos_servicemanager -c aos_servicemanager.cfg -v debug
```


# General setup

aos_servicemanager is required following applications to be available in the system or placed in
its working directory: runc, netns, wondershaper.
Install or put above applications to the working directory.

aos_servicemanager expects to aos_vis to be running and configured (see aos_vid readme).

# Setup Raspberry Pi 2:

## Prepare required applications

1. Install arm toolchain on your host pc:
```
sudo apt install gcc-5-arm-linux-gnueabi
```
2. Compile aos service manager:
```
cd $GOPATH/src/gitpct.epam.com/epmd-aepr/aos_servicemanager
CC=arm-linux-gnueabi-gcc-5 CGO_ENABLED=1 GOOS=linux GOARCH=arm GOARM=5 go build
```
3. Compile runc:
```
cd $GOPATH/src/github.com/opencontainers/runc
CC=arm-linux-gnueabi-gcc-5 CGO_ENABLED=1 GOOS=linux GOARCH=arm GOARM=5 go build
```
4. Compile netns:
```
cd $GOPATH/src/github.com/genuinetools/netns
CC=arm-linux-gnueabi-gcc-5 CGO_ENABLED=1 GOOS=linux GOARCH=arm GOARM=5 go build
```

## Prepare target

1. Install Raspberry image: https://www.raspberrypi.org/documentation/installation/installing-images/README.md
2. Enable ssh (see chapter 3 ): https://www.raspberrypi.org/documentation/remote-access/ssh/
3. Copy compiled files and configurations to following destination on the target:
* `$GOPATH/src/github.com/opencontainers/runc/runc` to `/usr/local/bin`
* `$GOPATH/src/github.com/genuinetools/netns/netns` to `/usr/local/bin`
* `$GOPATH/src/gitpct.epam.com/epmd-aepr/aos_servicemanager/aos_servicemanager/config/aos_servicemanager.service` to `/lib/systemd/system`
* `$GOPATH/src/gitpct.epam.com/epmd-aepr/aos_servicemanager/aos_servicemanager` to `/home/aos/servicemanager`
* `$GOPATH/src/gitpct.epam.com/epmd-aepr/aos_servicemanager/aos_servicemanager/fcrypt.json` to `/home/aos/servicemanager`
* `$GOPATH/src/gitpct.epam.com/epmd-aepr/aos_servicemanager/aos_servicemanager/data/*` to `/home/aos/servicemanager/data`
4. Change `fcrypt.json` to use `data` directory
5. Uncoment `net.ipv4.ip_forward=1` line in `/etc/sysctl.conf` (required for bridge)
6. Connect to the target by ssh with default credentials.
7. Create user: `aos` amd relogin with its credentials. 
8. Start aos_service manager and check service manager service:
```
sudo systemctl start aos_servicemanager
```
9. Check log with `journalctl`
10. To make service manager start automatically:
```
sudo systemctl enable aos_servicemanager
```
