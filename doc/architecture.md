# AOS Architecture

## Block diagram

![](images/architecture.png)

Devise contains following AOS components:
* AOS Service Manager (SM):
    * communicate with cloud
    * handle services life cycle
    * downloads and validates system update image

See [SM architecture](doc/servicemanager.md), documents for more details. 

On the unit side, SM interacts with the Identifier plugin in order to get unit systemID and current Users.  SM receives notification from identifier when Users changed. When it happens SM reconnects to the cloud with new parameters.

On the cloud side, SM communicates with:
* Service Discovery - to get Getaway connection info
* Gateway - to handle main exchange protocol
* Services CDN - to download service images

## Startup sequence

Startup sequence shows basic communication between different AOS parts:

```mermaid
sequenceDiagram
    participant Identifier
    participant SM as Service Manager
    participant Systemd
    participant SD as Service Discovery
    participant GW as Gateway
    participant CDN as Services CDN

    SM ->>+ Identifier: Get System ID, Users
    Identifier -->>- SM: System ID, Users
    SM ->>+ SD: Discovery
    SD -->>- SM: IoT Gateway info
    SM ->>+ GW: Current service list
    GW -->>- SM: New service list

    loop For each install service
        SM ->>+ CDN: Download image
        CDN -->>- SM: Image
        Note over SM: Install service
        SM ->> Systemd: Start service
        SM ->> GW: Install status
    end

    loop For each remove service
        SM ->> Systemd: Stop service
        Note over SM: Remove service
        SM ->> GW: Remove status
    end
```
