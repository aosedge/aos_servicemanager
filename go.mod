module github.com/aoscloud/aos_servicemanager

go 1.14

replace github.com/ThalesIgnite/crypto11 => github.com/aoscloud/crypto11 v1.0.3-0.20220217163524-ddd0ace39e6f

require (
	github.com/anexia-it/fsquota v0.1.3
	github.com/aoscloud/aos_common v0.0.0-20220218172038-8cf168776e9c
	github.com/apparentlymart/go-cidr v1.1.0
	github.com/containernetworking/cni v1.0.1
	github.com/containernetworking/plugins v1.0.1
	github.com/coreos/go-iptables v0.6.0
	github.com/coreos/go-systemd v0.0.0-20190719114852-fd7a80b32e1f
	github.com/coreos/go-systemd/v22 v22.3.2
	github.com/docker/docker v17.12.1-ce+incompatible // indirect
	github.com/fsnotify/fsnotify v1.4.9
	github.com/golang/protobuf v1.5.2
	github.com/google/uuid v1.3.0
	github.com/hashicorp/go-version v1.3.0 // indirect
	github.com/jinzhu/copier v0.3.4
	github.com/jlaffaye/ftp v0.0.0-20211117213618-11820403398b
	github.com/mattn/go-sqlite3 v1.14.9
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.0.2
	github.com/opencontainers/runc v1.0.3
	github.com/opencontainers/runtime-spec v1.0.3-0.20210326190908-1c3f411f0417
	github.com/pkg/errors v0.9.1
	github.com/shirou/gopsutil v3.21.11+incompatible
	github.com/sirupsen/logrus v1.8.1
	github.com/speijnik/go-errortree v1.0.1 // indirect
	github.com/tklauser/go-sysconf v0.3.9 // indirect
	github.com/vishvananda/netlink v1.1.1-0.20210330154013-f5de75959ad5
	github.com/vishvananda/netns v0.0.0-20211101163701-50045581ed74
	github.com/yusufpapurcu/wmi v1.2.2 // indirect
	golang.org/x/crypto v0.0.0-20210921155107-089bfa567519
	golang.org/x/mod v0.4.2
	golang.org/x/sys v0.0.0-20210816074244-15123e1e1f71
	google.golang.org/grpc v1.41.0
	google.golang.org/protobuf v1.27.1
)
