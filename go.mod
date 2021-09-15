module aos_servicemanager

go 1.14

replace github.com/ThalesIgnite/crypto11 => github.com/xen-troops/crypto11 v1.2.5-0.20210607075540-0b6da74b5450

require (
	github.com/StackExchange/wmi v1.2.1 // indirect
	github.com/anexia-it/fsquota v0.1.3
	github.com/apparentlymart/go-cidr v1.1.0
	github.com/containernetworking/cni v0.8.1
	github.com/containernetworking/plugins v0.9.1
	github.com/coreos/go-iptables v0.5.0
	github.com/coreos/go-systemd v0.0.0-20190719114852-fd7a80b32e1f
	github.com/coreos/go-systemd/v22 v22.3.2
	github.com/docker/docker v17.12.1-ce+incompatible // indirect
	github.com/fsnotify/fsnotify v1.4.7
	github.com/golang/protobuf v1.5.0
	github.com/google/uuid v1.1.2
	github.com/hashicorp/go-version v1.3.0 // indirect
	github.com/jinzhu/copier v0.3.2
	github.com/jlaffaye/ftp v0.0.0-20210307004419-5d4190119067
	github.com/mattn/go-sqlite3 v1.10.0
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.0.1
	github.com/opencontainers/runc v1.0.2
	github.com/opencontainers/runtime-spec v1.0.3-0.20210326190908-1c3f411f0417
	github.com/pkg/errors v0.9.1
	github.com/shirou/gopsutil v3.21.8+incompatible
	github.com/sirupsen/logrus v1.8.1
	github.com/speijnik/go-errortree v1.0.1 // indirect
	github.com/tklauser/go-sysconf v0.3.9 // indirect
	github.com/vishvananda/netlink v1.1.1-0.20201029203352-d40f9887b852
	github.com/vishvananda/netns v0.0.0-20200728191858-db3c7e526aae
	gitpct.epam.com/epmd-aepr/aos_common v0.0.0-20210921140409-99300ed4ec3e
	golang.org/x/crypto v0.0.0-20201221181555-eec23a3978ad
	golang.org/x/sys v0.0.0-20210816074244-15123e1e1f71
	google.golang.org/grpc v1.33.1
	google.golang.org/protobuf v1.26.0
)
