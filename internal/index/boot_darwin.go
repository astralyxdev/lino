package index

import "golang.org/x/sys/unix"

func readBootID() string {
	id, _ := unix.Sysctl("kern.bootsessionuuid")
	return id
}
