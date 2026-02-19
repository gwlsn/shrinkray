//go:build !linux

package browse

import "os"

func getDirSig(path string) (dirSig, error) {
	info, err := os.Stat(path)
	if err != nil {
		return dirSig{}, err
	}
	return dirSig{mtime: info.ModTime()}, nil
}
