module github.com/aosedge/aos_servicemanager

go 1.22.0

replace github.com/ThalesIgnite/crypto11 => github.com/aosedge/crypto11 v1.0.3-0.20220217163524-ddd0ace39e6f

replace github.com/coreos/go-iptables => github.com/aosedge/go-iptables v0.0.0-20220926113402-e57055a8a459

replace github.com/anexia-it/fsquota => github.com/aosedge/fsquota v0.0.0-20231127111317-842d831105a7

require (
	github.com/aosedge/aos_common v0.0.0-20241219165811-d19153261774
	github.com/containernetworking/cni v1.1.2
	github.com/containernetworking/plugins v1.3.0
	github.com/coreos/go-iptables v0.8.0
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf
	github.com/coreos/go-systemd/v22 v22.5.0
	github.com/golang/protobuf v1.5.4
	github.com/google/uuid v1.6.0
	github.com/hashicorp/go-version v1.7.0
	github.com/mattn/go-sqlite3 v1.14.24
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.0
	github.com/opencontainers/runc v1.2.3
	github.com/opencontainers/runtime-spec v1.2.0
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.9.3
	github.com/vishvananda/netlink v1.2.1-beta.2
	github.com/vishvananda/netns v0.0.4
	golang.org/x/exp v0.0.0-20230315142452-642cacee5cc0
	golang.org/x/mod v0.21.0
	golang.org/x/sys v0.28.0
	google.golang.org/grpc v1.69.0
	google.golang.org/protobuf v1.36.0

)

require (
	github.com/ThalesIgnite/crypto11 v0.0.0-00010101000000-000000000000 // indirect
	github.com/anexia-it/fsquota v0.0.0-00010101000000-000000000000 // indirect
	github.com/cavaliergopher/grab/v3 v3.0.1 // indirect
	github.com/cyphar/filepath-securejoin v0.3.5 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/golang-migrate/migrate/v4 v4.18.1 // indirect
	github.com/google/go-tpm v0.9.3 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/miekg/pkcs11 v1.0.3-0.20190429190417-a667d056470f // indirect
	github.com/moby/sys/mountinfo v0.7.1 // indirect
	github.com/moby/sys/userns v0.1.0 // indirect
	github.com/safchain/ethtool v0.5.9 // indirect
	github.com/seccomp/libseccomp-golang v0.10.0 // indirect
	github.com/shirou/gopsutil v3.21.11+incompatible // indirect
	github.com/speijnik/go-errortree v1.0.1 // indirect
	github.com/stefanberger/go-pkcs11uri v0.0.0-20230803200340-78284954bff6 // indirect
	github.com/thales-e-security/pool v0.0.2 // indirect
	github.com/tklauser/go-sysconf v0.3.14 // indirect
	github.com/tklauser/numcpus v0.8.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	go.uber.org/atomic v1.7.0 // indirect
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/net v0.30.0 // indirect
	golang.org/x/text v0.21.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20241015192408-796eee8c2d53 // indirect
)
