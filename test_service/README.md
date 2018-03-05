This is reference service.

Steps to make a custom service:

1. Put service binary and data to home/service folder. This folder will be
   mounted to /home/service folder on the target;
   
2. Update config.json according to service needs. For example, assign startup application:

```
	"process": {
		"args": [
			"/home/service/service_app"
		]
	}
```

Network setup:

If services require network, netns should be installed on the host:

1. go get github.com/jessfraz/netns
2. cd ${GOPATH}/go/src/github.com/jessfraz/netns/
3. sudo make

Update resolv.conf to match network configuration.