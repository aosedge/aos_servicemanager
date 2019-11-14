# UM Client

UM Client provides API to communicate with Update Manager (UM). The main function of UM Client:
* downloads, validates and unpacks the update image
* request UM for systemd update/revert
* provides status of update/revert to the AOS cloud

Sequence diagram of system update:

```mermaid
sequenceDiagram
    participant UM
    participant SM
    participant AOS as AOS Cloud

    SM ->>+ UM: Get System Version
    UM ->>- SM: System Version
    SM ->>+ AOS: Send System Version
    AOS ->>+ SM: System Update
    SM ->>+ AOS: Download Update Image
    AOS ->>- SM: Image
    Note over SM: Validate and unpack image
    SM ->>+ UM: Update
    UM ->>- SM: Status
    SM ->>+ AOS: Update Status
```

Sequence diagram of system revert:

```mermaid
sequenceDiagram
    participant UM
    participant SM
    participant AOS as AOS Cloud

    SM ->>+ UM: Get System Version
    UM ->>- SM: System Version
    SM ->>+ AOS: Send System Version
    AOS ->>+ SM: System Revert
    SM ->>+ UM: Revert
    UM ->>- SM: Status
    SM ->>+ AOS: Revert Status
```
