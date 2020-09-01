# Downloader

Downloader package covers next functionality:

* download package
* resume download in case of interrupt
* send download alerts
* validate file checksums
* decrypt downloaded package
* validate file signature  

Downloader sends [alerts messages](https://kb.epam.com/display/EPMDAEPRA/Alerts+message) with the next payload format:

Start download alert message

```text
url: https://edge-fusion-poc-2.azureedge.net/vehicle_update/00001280--73-30.enc?rDbBEKCbjBJTcp1_wDSHhf9Rl2KdCZ8NMJulrnGr88EvkaXKEPOitJAh1ssTUiKc72yMV92yAODuDt4VHFQKau48HAIcpTXcMOczmulI2V9EYpFJ1X5dYz1aVkXFcy57FQ
message: Download started
progress: 0%
totalBytes: 592B
downloadedBytes: 0B
```

Finish download alert message

```text
url: https://edge-fusion-poc-2.azureedge.net/vehicle_update/00001280--73-30.enc?rDbBEKCbjBJTcp1_wDSHhf9Rl2KdCZ8NMJulrnGr88EvkaXKEPOitJAh1ssTUiKc72yMV92yAODuDt4VHFQKau48HAIcpTXcMOczmulI2V9EYpFJ1X5dYz1aVkXFcy57FQ
message: Download finished code: 200
progress: 100%
totalBytes: 592B
downloadedBytes: 592B
```

Download status alert message is sent each 30 seconds during download

```text
url: https://edge-fusion-poc-2.azureedge.net/containers/8dc0c1d0-aac9-4d9d-89b8-fed0ac5fc7b2/1098.enc?PI0XE0SApCdPFkukFrHJmtBZ48QCxgpsrI6NQS-X6ksj_rt2bukU6SvmO8-LmIrLpPpnqEII3GD68oP_aPCubWp5B3KPdH8hH_yY1RVC-JPKXkz_HdR6FR3eAl1co27gQxZAy_eGDN3SHZ31LtRON98t5xCLBG4
message: Download status
progress: 46%
total_bytes: 101.8Mb
downloaded_bytes: 47.2Mb
```

Download interrupted message

```text
url: https://edge-fusion-poc-2.azureedge.net/containers/8dc0c1d0-aac9-4d9d-89b8-fed0ac5fc7b2/1098.enc?BGiV6spp2xhPZvOy8eECoGhhsNq1ZKYMFvvbKRdbBCgtfS11ooLdqQT_okkGmwQp8iy-bUG_RkMZf_l2ESBK9GChXewnomY-qfGLHwpI4gmFae_yIVkDA91grhEf3aYZMeovGNgEI5oB3f5vJtDDSjN1AdXbt3k
message: Download interrupted reason: Connection errors
progress: 6%
total_bytes: 101.8Mb
downloaded_bytes: 6.2Mb
```

Download resumed message

```text
url: https://edge-fusion-poc-2.azureedge.net/containers/8dc0c1d0-aac9-4d9d-89b8-fed0ac5fc7b2/1098.enc?ntHHhTh8DQkjnrJE5Yqs8luUIYUm0-Eqbvu3xqazdw2lDGTSLz0Rz4uS6r0jb3uEXygbGvtfJRJ1BlcmLEpIdZowV9KmncNEt9zSJCsZnKu4qcflSHrFv75j79gNlwR8cj-24kp81UAmegOHbgVIc0wF3pz6UAw
message: Download resumed reason: Internet connection error
progress: 92%
total_bytes: 101.8Mb
downloaded_bytes: 94.4Mb
```
