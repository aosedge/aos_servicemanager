# Monitoring

Monitoring package monitors system resources used by system itself and used by each running AOS service.

Monitoring has two different timers: poll timer and send timer. Period of these timers are defined in [config](doc/config.md) with `monitoring.pollPeriod` and `monitoring.sendPeriod`. On each poll timer tick, monitoring gets current usage statistics and processes alert rules. On each send timer tick, it sends monitoring data to the cloud.

## System resource monitoring

Monitoring system resource is quite simple: each poll period `monitoring` gets system CPU, RAM, disk, traffic usage, processes alerts and stores these values in monitoring data which is sent on each send period.

## Service resource monitoring

When AOS service is started, `launcher` passes to `monitoring` parameters of started service: service PID, service IP address and service storage folder. `monitoring` adds this info to a list of monitoring services. Then, on poll period, `monitoring` gets CPU, RAM used by process with service PID and size of the storage folder. In same way as it is done for system resource usage, `monitoring` stores these values in monitoring data and sends monitoring data on send period.

When AOS service is stopped, `launcher` notifies `monitoring` and it removes the stopped service from the monitoring list.

## Traffic monitoring

As we need to count only internet traffic, we can't use a network interface statistics. Because it includes also local traffic going between different systems components.

To filter local traffic `monitoring` uses iptables. It creates special iptables chains to count only internet traffic. The following network ranges are treated as local and not counted as internet traffic:
* 127.0.0.0/8
* 10.0.0.0/8
* 172.16.0.0/12
* 192.168.0.0/16
* netns bridge addresses (default 172.19.0.0/16)

After sending monitoring data to the cloud, traffic monitoring values are stored in the [database](doc/database.md) in order to continue count after power cycle, reboot etc.

Traffic is counted in per day basis.

## Traffic limit

`monitoring` not only counts traffic but also controls traffic limits with iptables. When service traffic limit is reached, `monitoring` redirects appropriate chain to rejected state. As result service can't send and receive any packets from outside network. Traffic limit is also set in per day basis.
