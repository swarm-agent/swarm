package main

import "golang.org/x/sys/unix"

const secretGetTermios = unix.TCGETS
const secretSetTermios = unix.TCSETS
