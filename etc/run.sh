#!/bin/sh
nohup /usr/local/bin/plugin >/var/log/plugin.out 2>&1 &
/usr/local/bin/coredns -conf /usr/local/bin/Corefile >/var/log/coredns.out 2>&1
