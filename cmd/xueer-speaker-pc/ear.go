//go:build windows

package main

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"github.com/hardware661/xueer-speaker/internal/pcagent"
)

// earDictating says whether the owner is in a xueer-ear dictation round
// (%LOCALAPPDATA%\XueerEar\status.json). The file is opened with every
// share mode: xueer-ear replaces it by renaming a new copy over it, which
// would fail while we held it without FILE_SHARE_DELETE.
func earDictating() bool {
	path := filepath.Join(os.Getenv("LOCALAPPDATA"), "XueerEar", "status.json")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return false
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<10))
	if err != nil {
		return false
	}
	return pcagent.EarDictating(data, time.Now())
}
