Build instruction:

```
go get github.com/cavaliercoder/grab
go get github.com/streadway/amqp

go build servicemanager.go
```


Fcrypt configuration:

`config/fcrypt.json` contains example configuration for fcrypt library. Library expects to find this `fcrypt.json` in the current working directory.

TODO: add support for `$GOPATH` (or at least for `~`) in paths defined in `fcrypt.json`
