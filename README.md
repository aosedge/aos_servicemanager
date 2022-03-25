
# AOS Service Manager  

[![CI](https://github.com/aoscloud/aos_servicemanager/workflows/CI/badge.svg)](https://github.com/aoscloud/aos_servicemanager/actions?query=workflow%3ACI)
[![codecov](https://codecov.io/gh/aoscloud/aos_servicemanager/branch/main/graph/badge.svg?token=mZKEdNf2fx)](https://codecov.io/gh/aoscloud/aos_servicemanager)
[![Quality Gate Status](https://sonarcloud.io/api/project_badges/measure?project=aoscloud_aos_servicemanager&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=aoscloud_aos_servicemanager)


Aos Service Manager (SM) is a part of Aos system which resides on the device side and stands for the following tasks:

* communicate with the communication manager;
* install, remove, start, stop Aos services;
* configure Aos services network;
* configure and monitor Aos services and system resource usage;
* provide persistent storage and state handling for Aos services.

See architecture [document](doc/architecture.md) for more details.

# Build

## Required GO packages

All requires GO packages exist under `vendor` folder. Content of this folder is created with GO modules:

```bash
export GO111MODULE=on
```

```golang
go mod init
go mod vendor
```

## Required native packages

* libsystemd-dev

## Native build

```
go build
```

## ARM 64 build

Install arm64 toolchain:
```
sudo apt install gcc-aarch64-linux-gnu
```
Build:

```
CC=aarch64-linux-gnu-gcc CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build
```

# Configuration

SM is configured through a configuration file. The file `aos_servicemanager.cfg` should be either in current directory or specified with command line option as following:
```
./aos_servicemanager -c aos_servicemanager.cfg
```
The configuration file has JSON format described [here] (doc/config.md). Example configuration file could be found in `misc/config/aos_servicemanager.cfg`

To increase log level use option -v:
```
./aos_servicemanager -c aos_servicemanager.cfg -v debug
```

# System folders and files mount

For each installed service, SM mounts following system folders:
* `/bin`
* `/sbin`
* `/lib`
* `/usr`
* `/lib64` - if exists
* `/tmp` - this is temporary solution. Each service should has its own tmp folder
* `/etc/ssl` - this is temporary solution. Each service should be provided with its own certificates

To configure service network, SM mounts following system files:
* `/etc/hosts`
* `/etc/resolv.conf`
* `/etc/nsswitch.conf`

To override system network configuration, a custom version of the above files can be put to SM working directory under `etc` folder.

# Run

## Required packages

SM needs CM and IAM to be running and configured (see [CM](https://github.com/aoscloud/aos_communicationmanager#readme) and [IAM](https://github.com/aoscloud/aos_iamanager#readme)) before start.

SM requires following applications to be available in the system or placed in SM working directory:
* [runc](https://github.com/opencontainers/runc) or [crun](https://github.com/containers/crun) - launch service containers
## Test required packages

* [libssl-dev] - headers for TPM simulator
* [pyftpdlib] - light python ftp server

## Required Python3 packages

Following python3 packages are required to launch demo python service:
* `python3-compression`
* `python3-crypt`
* `python3-enum`
* `python3-json`
* `python3-misc`
* `python3-selectors`
* `python3-shell`
* `python3-six`
* `python3-threading`
* `python3-websocket-client`

Following python3 packages are required to launch telemetry-emulator:
* `python3-argparse`
* `python3-compression`
* `python3-datetime`
* `python3-json`
* `python3-misc`
* `python3-netserver`
* `python3-selectors`
* `python3-shell`
* `python3-signal`
* `python3-textutils`
* `python3-threading`

# Test

Install all necessary dependencies:
```
./ci/setup_env.sh
```

Test all packages:

```
sudo env "PATH=$PATH" go test ./... -v
```