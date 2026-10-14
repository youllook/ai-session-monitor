package main

import "syscall"

// shortPath 取 Windows 8.3 短路徑（沒有空格）；拿不到就原樣回傳
func shortPath(p string) string {
	in, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, 1024)
	n, err := syscall.GetShortPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return syscall.UTF16ToString(buf[:n])
}
