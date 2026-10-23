package main

import "golang.org/x/sys/unix"

const secretGetTermios = unix.TIOCGETA
const secretSetTermios = unix.TIOCSETA
