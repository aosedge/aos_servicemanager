app_downloader implements
- communication with amqp client via WebSockets
- download wgt file based on information from amqp client
- install, uninstall , run , stop containers using agl app framework 

Build instruction:

```
go get github.com/cavaliercoder/grab

cd aos_lifecycle_manager
export GOPATH=$GOPATH:$PWD
go build servicemanager.go
```
