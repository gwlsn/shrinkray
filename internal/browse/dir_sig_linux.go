//go:build linux

package browse

import (
	"os"
	"syscall"
	"time"
)

func getDirSig(path string) (dirSig, error) {
	info, err := os.Stat(path)
	if err != nil {
		return dirSig{}, err
	}
	sig := dirSig{mtime: info.ModTime()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		sig.inode = stat.Ino
		sig.dev = stat.Dev
		sig.ctime = time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec)
	}
	return sig, nil
}
