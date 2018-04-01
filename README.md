# Build instruction:

```
go get github.com/cavaliercoder/grab
go get github.com/streadway/amqp

go get github.com/sirupsen/logrus github.com/mattn/go-sqlite3 github.com/opencontainers/runtime-spec/specs-go github.com/containerd/go-runc

go build servicemanager.go

for testing
go get gopkg.in/jarcoal/httpmock.v1

```


Fcrypt configuration:

`config/fcrypt.json` contains example configuration for fcrypt library. Library expects to find this `fcrypt.json` in the current working directory.

TODO: add support for `$GOPATH` (or at least for `~`) in paths defined in `fcrypt.json`


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
