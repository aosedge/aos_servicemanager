# Logging

Logging provides the service log to the cloud. In current design service `stdout` is redirected to the systemd journal. The logging package parses the journal and provides log filtered by service id.

There are two types of request:
* log for requested period
* crash log - last service run log between service start and service crash
