# AOS Service Manager architecture

AOS Service Manager contains following packages:
* [config](doc/config.md) - provides configuration for all SM packages
* fcrypt - used by other packeges to encrypt/decrypt data
* database - stores local services configuration
* amqphandler - handles communication with IoT Gateway
* launcher - installs/removes AOS services and manages their life cycle
* monitoring - monitors system and AOS services resource usage
* dbushandler - provides VIS client permissions

```mermaid
graph BT
    dbushandler --- VIS
    amqphandler --- Cloud
subgraph AOS Service Manager
    config
    fcrypt
    launcher --- database
    launcher --- amqphandler
    monitoring --- database
    monitoring --- amqphandler
    dbushandler --- database
end
```
