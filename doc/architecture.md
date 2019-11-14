# AOS Architecture

## Block diagram

![](images/architecture.svg)

Vehicle contains following AOS components:
* AOS Service Manager (SM):
    * communicate with cloud
    * handle services life cycle
    * provides VIS permissions for VIS clients
    * downloads and validates system update image
* AOS VIS (VIS):
    * provides access to the vehicle data
* AOS Update Manager (UM):
    * applies updates for different system components

See [SM architecture](doc/servicemanager.md), [VIS architecture](), [UM architecture]() documents for more details. 

On the vehicle side, SM interacts with VIS in order to get vehicle VIN and current Users. This is done through WSS protocol. SM receives notification from VIS when Users changed. When it happens SM reconnects to the cloud with new parameters. VIS is connected with SM through D-Bus to get VIS permissions for VIS clients.

On the cloud side, SM communicates with:
* Service Discovery - to get Getaway connection info
* Gateway - to handle main exchange protocol
* Services CDN - to download service images

## Startup sequence

Startup sequence shows basic communication between different AOS parts:

```mermaid
sequenceDiagram
    participant VIS
    participant SM as Service Manager
    participant Systemd
    participant SD as Service Discovery
    participant GW as Gateway
    participant CDN as Services CDN

    SM ->>+ VIS: Get VIN, Users
    VIS -->>- SM: VIN, Users
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
