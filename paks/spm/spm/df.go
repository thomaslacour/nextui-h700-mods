package main

// df.go — free space of a directory, via Statfs. One implementation serves
// both linux (the console) and darwin (the tests): the fields exist on both.

import "syscall"

func statfsFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
