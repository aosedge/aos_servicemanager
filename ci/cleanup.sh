#!/bin/sh

find ./ -name "tmp" -type d -exec rm -rf {} +
ip -all netns delete
iptables --flush
iptables -t nat --flush
iptables -t nat -X