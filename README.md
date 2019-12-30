
# AOS Service Manager

AOS Service Manager (SM) is a part of AOS system which resides on the vehicle side and stands for following tasks:
* communicate with the backend;
* install, remove, start, stop AOS services;
* configure AOS services network;
* configure and monitor AOS services and system resource usage;
* provide persistent storage and state handling for AOS services.

See architecture [document](doc/architecture.md) for more details.

# Build

## Required GO packages

Dependency tool `dep` (https://github.com/golang/dep) is used to handle external package dependencies. `dep` tool should be installed on the host machine before performing the build. `Gopkg.toml` contains list of required external go packages. Perform following command before build to fetch the required packages:

```
dep ensure
```

## Required native packages

* libsystemd-dev

## Identification module selection

For authentication with the cloud SM requires to get the `system id` and `user claims` parameters. These parameters are platform specific. All supported modules are located under `identification` folder. Registration of modules are done in `register<name>module.go` files, where `name` is the module name (for example: `registervismodule.go`). Each register module file contains the build tag which should be used during build to include the module into the final binary:
```
// +build with_vis_module
```
Build tags are specified by `--tags` parameters in the build command:

```
go build --tags "with_<name1>_module with_<name2>_module"
```

Any number of the supported modules can be built with final binary. But only one is used in runtime. The selection is done with [config](doc/config.md) file.

## Native build

```
go build --tags "with_vis_module"
```

## ARM 64 build

Install arm64 toolchain:
```
sudo apt install gcc-aarch64-linux-gnu
```
Build:

```
CC=aarch64-linux-gnu-gcc CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build --tags "with_vis_module"
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

In order to override system network configuration, custom version of above files can be put to SM working directory under `etc` folder.

# Run

## Required packages

SM needs aos_vis to be running and configured (see aos_vis [readme](https://gitpct.epam.com/epmd-aepr/aos_vis/blob/master/README.md)) before start.

SM requires following applications to be available in the system or placed in SM working directory:
* [runc](https://github.com/opencontainers/runc) - launch service containers
* [netns](https://github.com/genuinetools/netns) - set containers network bridge
* [wondershaper](https://github.com/magnific0/wondershaper) - set network UL/DL speed limit

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

To launch test, additional go packages should be installed:
* github.com/jlaffaye/ftp

Test all packages:

```
sudo -E go test ./... -v
```
## Known issues
* dbushandler test fails if run with sudo
