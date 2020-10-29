# Database

Database provides API to store/retrieve local services configuration and other persistent SM data. Database consists of following tables:

* `config` - store AOS system current stat, version
* `services` - stores AOS services configuration
* `users` - stores AOS users configuration and their services
* `trafficmonitor` - stores accumulated traffic monitor statistics
* `layers` - store information about installed service's layers

The tables have following format:

## `config` table  

| Field Name    | Type      | Key | Description                                 |
|---------------|-----------|-----|---------------------------------------------|
| version       | INTEGER   |     | Curren system version                       |
| cursor        | TEXT      |     |                                             |

## `services` table

The table stores info about AOS services installed in the system.

| service       | TEXT      |     | Systemd service name                        |

| Field Name    | Type      | Key | Description                                 |
|---------------|-----------|-----|---------------------------------------------|
| id            | TEXT      | *   | Service ID                                  |
| version       | INTEGER   |     | Service version                             |
| serviceProvide| TEXT      |     | Service provider name                       |
| path          | TEXT      |     | Location of service image                   |
| unit          | TEXT      |     | Service unit name                           |
| user          | TEXT      |     | Linux system user to run service            |
| hostname      | TEXT      |     | Hostname of the service                     |
| permissions   | TEXT      |     | Service VIS permissions                     |
| state         | INTEGER   |     | Service state: stopped, running etc.        |
| status        | INTEGER   |     | Service status                              |
| startat       | TIMESTAMP |     | Timestamp at which service was started      |
| ttl           | INTEGER   |     | Service time to live in days                |
| alertRules    | TEXT      |     | Service alert rules                         |
| ulLimit       | INTEGER   |     | Uplink traffic limit                        |
| dlLimit       | INTEGER   |     | Downlink traffic limit                      |
| ulSpeed       | INTEGER   |     | Uplink traffic speed limit                  |
| dlSpeed       | INTEGER   |     | Downlink traffic speed limit                |
| storageLimit  | INTEGER   |     | Storage limit                               |
| stateLimit    | INTEGER   |     | State limit                                 |
| layerList     | TEXT      |     | List of layers for the service              |
| deviceResources|TEXT      |     | List of HW resources for the service        |

## `users` table

The table keeps info about users and services used by them.

| Field Name    | Type      | Key | Description                                 |
|---------------|-----------|-----|---------------------------------------------|
| users         | TEXT      | *   | Store users in text representation          |
| serviceid     | TEXT      | *   | Service ID                                  |
| storageFolder | TEXT      |     | Users service storage folder                |
| stateCheckSum | BLOB      |     | Users service state checksum                |

## `trafficmonitor` table

The table is used to store current traffic statistics to do not lose it on SM restart.

| Field Name    | Type      | Key | Description                                 |
|---------------|-----------|-----|---------------------------------------------|
| chain         | TEXT      | *   | Iptables chain on which traffic is counted  |
| time          | TIMESTAMP |     | Time when this value was updated            |
| value         | INTEGER   |     | Traffic value                               |

## `layers` table  

 The table is used to store information about installed service's layers

| Field Name    | Type      | Key | Description                                 |
|---------------|-----------|-----|---------------------------------------------|
| digest        | TEXT      | *   | Layer unique digest                         |
| layerId       | TEXT      |     | Layer human-readable identification         |
| path          | TEXT      |     | Location of the layer on FS                 |
| osVersion     | TEXT      |     | Compatible system version                   |
