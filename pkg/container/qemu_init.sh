#!/bin/busybox sh
set -e

/bin/mount -t proc proc /proc
/bin/mount -t sysfs sys /sys
/bin/mount -t devtmpfs devtmpfs /dev

/bin/mkdir /dev/pts
/bin/mount -t devpts devpts -o noexec,nosuid,newinstance,ptmxmode=0666,mode=0620,gid=tty /dev/pts/
/bin/mount --bind /dev/pts/ptmx /dev/ptmx

# We can ignore this fail, if we use a kernel with kvm_guest.config, we won't need this
# and network will work anyway
# If this fails and we won't have network, the ifconfig command will fail anyway.
/usr/sbin/modprobe virtio_net || :

/bin/ifconfig lo up
/bin/ifconfig eth0 10.0.2.15 netmask 255.255.255.0
/bin/route add default gw 10.0.2.2 eth0
/bin/hostname "$(cat /etc/hostname)"

/usr/bin/ssh-keygen -A
/usr/sbin/sshd -D
