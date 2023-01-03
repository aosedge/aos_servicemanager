module github.com/aoscloud/aos_servicemanager

go 1.18

replace github.com/ThalesIgnite/crypto11 => github.com/aoscloud/crypto11 v1.0.3-0.20220217163524-ddd0ace39e6f

replace github.com/coreos/go-iptables => github.com/aoscloud/go-iptables v0.0.0-20220926113402-e57055a8a459

require (
	github.com/aoscloud/aos_common v0.0.0-20230103111123-d1f5bca99a33
	github.com/apparentlymart/go-cidr v1.1.0
	github.com/containernetworking/cni v1.1.1
	github.com/containernetworking/plugins v1.1.1
	github.com/coreos/go-iptables v0.6.0
	github.com/coreos/go-systemd v0.0.0-20190719114852-fd7a80b32e1f
	github.com/coreos/go-systemd/v22 v22.3.2
	github.com/golang/protobuf v1.5.2
	github.com/google/uuid v1.3.0
	github.com/mattn/go-sqlite3 v1.14.13
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.0.1
	github.com/opencontainers/runc v1.1.2
	github.com/opencontainers/runtime-spec v1.0.3-0.20210326190908-1c3f411f0417
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.8.1
	github.com/vishvananda/netlink v1.1.1-0.20210330154013-f5de75959ad5
	github.com/vishvananda/netns v0.0.0-20211101163701-50045581ed74
	golang.org/x/exp v0.0.0-20221229233502-02c3fc3b3eb4
	golang.org/x/mod v0.6.0
	golang.org/x/sys v0.1.0
	google.golang.org/grpc v1.46.2
	google.golang.org/protobuf v1.28.0
)

require (
	github.com/ThalesIgnite/crypto11 v0.0.0-00010101000000-000000000000 // indirect
	github.com/anexia-it/fsquota v0.1.3 // indirect
	github.com/bwesterb/go-xentop v1.0.1 // indirect
	github.com/cavaliergopher/grab/v3 v3.0.1 // indirect
	github.com/cyphar/filepath-securejoin v0.2.3 // indirect
	github.com/docker/docker v17.12.1-ce+incompatible // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/godbus/dbus/v5 v5.0.6 // indirect
	github.com/golang-migrate/migrate/v4 v4.15.0 // indirect
	github.com/google/go-tpm v0.3.3 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-version v1.5.0 // indirect
	github.com/lib/pq v1.10.6 // indirect
	github.com/miekg/pkcs11 v1.0.3 // indirect
	github.com/moby/sys/mountinfo v0.5.0 // indirect
	github.com/safchain/ethtool v0.0.0-20210803160452-9aa261dae9b1 // indirect
	github.com/seccomp/libseccomp-golang v0.9.2-0.20210429002308-3879420cc921 // indirect
	github.com/shirou/gopsutil v3.21.11+incompatible // indirect
	github.com/speijnik/go-errortree v1.0.1 // indirect
	github.com/thales-e-security/pool v0.0.2 // indirect
	github.com/tklauser/go-sysconf v0.3.10 // indirect
	github.com/tklauser/numcpus v0.4.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.2 // indirect
	go.uber.org/atomic v1.7.0 // indirect
	golang.org/x/crypto v0.1.0 // indirect
	golang.org/x/net v0.1.0 // indirect
	golang.org/x/text v0.4.0 // indirect
	google.golang.org/genproto v0.0.0-20220314164441-57ef72a4c106 // indirect
)
