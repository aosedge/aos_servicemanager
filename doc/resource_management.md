# Resource management

Resource management is done by `runc`. Runc uses `rlimits` and `cgroups` to manage host’s resources. We should use both approaches to make AOS services safe and secure. `rlimits` and `cgroups` configuration are done through runtime OCI specification: `config.json`. To configure `rlimits` put `rlimits` array under `process` object. Each element of the array has following structure:
* `type` - type of rlimit as defined in GETRLIMIT(2) man pages
* `soft` - the value of the limit enforced for the corresponding resource
* `hard` - the ceiling for the soft limit that could be set by an unprivileged process

Example:
```json
{
    "ociVersion": "0.5.0-dev",
    "process": {
 
        ...
     
    "rlimits": [
        {
            "type": "RLIMIT_CORE",
            "hard": 0,
            "soft": 0
        },
        {
            "type": "RLIMIT_NOFILE",
            "hard": 10,
            "soft": 10
        }
    ],
```
`cgroup` configuration is located under `resources` root object and has different format for different resource type.

Example:
```json
"resources": {
    "memory": {
        "limit": 100000,
        "reservation": 200000
    },
    "devices": [
        {
            "allow": false,
            "access": "rwm"
            }
    ]
}
```
## Resources to be limited

### Core file size

By default we should disable core file by putting 0 to hard and soft parameter:
```json
{
    "type": "RLIMIT_CORE",
    "hard": 0,
    "soft": 0
}
```

### Maximum number of opened file descriptors

By default we should limit number of opened file descriptors to 10 (TBD)

```json
{
    "type": "RLIMIT_NOFILE",
    "hard": 10,
    "soft": 10
}
```

(TBD what else to limit from rlimits)

### Devices

Access should by provided and handled by [Device Manager](./devicemanager.md) module.

### Memory

To limit memory available for the container, following items in “memory” object shall be set:
* `limit` - limit of memory usage
* `swap` - limit of memory+swap usage

(TBD which one limit and what should be default value)

```json
"memory": {
    "limit": 1024,
    "swap": 2048
}
```

### CPU

There are two ways to limit CPU usage:
* relative, using cpu `shares` value: cpu bandwidth will be divided between services according to shares value. For example: container with shares equal to 100 will have 10% of cpu with shares equal to 1000
* absolute, using cpu `quota` and `period`: default period for one CPU is 100000. So to allow container to have maximum 50% of on CPU, following parameters should be set: period = 100000, quota = 50000. To use maximum one and half CPU on multi CPU system: period = 100000, quota = 150000

Also CPUs used by the container can be limited too by setting `cpus` value.

Example:

```json
"cpu": {
    "shares": 1024,
    "quota": 100000,
    "period": 50000,
    "cpus": "2-3"
}
```

We use absolute value.

### Block IO

IO operations can be limited with weight value which is relative (bigger weight allows more IO operations compared to smaller weight value) or with absolute values in bytes per second for read/write operations.

Absolute value is more convenient but requires to specify the device. It means the backend should be aware about HW on the device. Or AOS Service manager should put the device info during install.

(TBD in which way to limit block IO, which devices and how to define them)

Example:

```json
"blockIO": {
    "weight": 1000,
    "throttleReadBpsDevice": [
        {
            "major": 8,
            "minor": 0,
            "rate": 600
        }
    ],
    "throttleWriteBpsDevice": [
        {
            "major": 8,
            "minor": 0,
            "rate": 600
        }
    ]
}
```

### Network

There is no mechanism to limit network usage. Network limits implemented on device side with different mechanisms. Limit values should be set under `annotations` item:
* `com.epam.aos.network.uploadSpeed` - limits service upload speed in kbps
* `com.epam.aos.network.downloadSpeed` - limits service download speed in kbps
* `com.epam.aos.network.uploadLimit` - number of upload bytes per day
* `com.epam.aos.network.downloadLimit` - number of download bytes per day

For example:

```json
"annotations": {
    "com.epam.aos.network.uploadSpeed": "2048",
    "com.epam.aos.network.downloadSpeed": "2048",
    "com.epam.aos.network.uploadLimit": "65536",
    "com.epam.aos.network.downloadLimit": "65536",
}
```

### Service disk size

Service manager provides RW folder (local storage) to the service in order to store any persistent data. Each users on service has its own local storage. To limit maximum size of local storage allocated for all users, following parameter in `annotations`  section should be set:
* `com.epam.aos.storage.limit` - maximum size of users local storages. If 0 or not present, then service has no local storage
  
Also service manager provides special file with state info which is also stored inside local storage. The size of this file could be limited with following parameter in `annotaions`:
* `com.epam.aos.state.limit` - maximum size of the state file. If 0 or not present, then service has no state file

For example:

```json
"annotations": {
    "com.epam.aos.storage.limit": "65536",
    "com.epam.aos.state.limit": "8192"
 }
```

### System folders mount

Service manager adds mounting of system folder and some system files during service install. The list of mounting system folders and files:

| Dir                |Mode| Comments |
|--------------------|----|-|
| /bin               | RO | |
| /sbin              | RO | |
| /lib               | RO | |
| /lib64             | RO | |
| /usr               | RO | |
| /tmp               | RW | |
| /etc/ssl           | RO | |
|                    |    | |
| /etc/hosts         | RO | /etc/hosts will be mounted only if this file is missing in <working dir>/etc/hosts. It allows to have service configuration different from system.|
| /etc/resolv.conf   | RO | Same as above |
| /etc/nsswitch.conf | RO | Same as above |

### /tmp folder

Currently all services share host `tmp` folder. But it is not the right solution because some services may have same files in the `tmp`. To resolve this issue each service should have `tmp` folder allocated in the memory. We should specify default size (TBD) and set mount accordingly:

```json
"mounts": [
    {
        "destination": "/tmp",
        "type": "tmpfs",
        "source": "tmpfs",
        "options": ["nosuid","strictatime","mode=755","size=65536k"]
    }
]
```

### /etc/ssl

Currently we mount host’s `/etc/ssl` folder. But each service which requires this folder should have its own certificates (TBD).

