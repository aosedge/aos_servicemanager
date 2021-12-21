
# AOS Service Manager  

[![CI](https://github.com/aoscloud/aos_servicemanager/workflows/CI/badge.svg)](https://github.com/aoscloud/aos_servicemanager/actions?query=workflow%3ACI)

AOS Service Manager (SM) is a part of AOS system which resides on the vehicle side and stands for the following tasks:

* communicate with the backend;
* install, remove, start, stop AOS services;
* configure AOS services network;
* configure and monitor AOS services and system resource usage;
* provide persistent storage and state handling for AOS services.

See architecture [document](doc/architecture.md) for more details.

# Build

## Required GO packages

All requires GO packages exist under `vendor` folder. Content of this folder is created with GO modules:

```bash
export GO111MODULE=on
export GOPRIVATE=gitpct.epam.com/*
```

```golang
go mod init
go mod vendor
```

## Required native packages

* libsystemd-dev

## Identification module selection

For authentication with the cloud, SM requires to get the `system id` and `user claims` parameters. These parameters are platform-specific. There are identification packages under `identification` folder. One of identification instance should be explicitly created in the  main function:

```golang
	// Create identifier
	// Use appropriate identifier from identification folder
	if sm.identifier, err = visidentifier.New(cfg.Identifier); err != nil {
		goto err
	}
```

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

SM needs aos_vis to be running and configured (see aos_vis [readme](https://gitpct.epam.com/epmd-aepr/aos_vis/blob/master/README.md)) before start.

SM requires following applications to be available in the system or placed in SM working directory:
* [runc](https://github.com/opencontainers/runc) - launch service containers
* [wondershaper](https://github.com/magnific0/wondershaper) - set network UL/DL speed limit
* [iperf](https://sourceforge.net/projects/iperf2/) - a network traffic tool for measuring TCP and UDP performance. 

## Test required packages

* [libssl-dev]  - headers for TPM simulator
* [rabbitmq-server] - AMQP server 
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
sudo -E go test ./... -v
```