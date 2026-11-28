package crowdankifs

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func openRegular(dir string) (*os.File, error) {
	path := strings.ReplaceAll(filepath.Join(dir, deckJSON), "/", "\\")
	if strings.HasPrefix(path, `\\.\`) {
		return nil, errWrongType
	}
	volume := filepath.VolumeName(path)
	unc := strings.HasPrefix(volume, `\\`) && !strings.HasPrefix(volume, `\\?\`)
	extendedUNC := strings.HasPrefix(strings.ToUpper(volume), `\\?\UNC\`)
	if unc || extendedUNC {
		share := volume[strings.LastIndexByte(volume, '\\')+1:]
		if strings.EqualFold(share, "pipe") || strings.EqualFold(share, "mailslot") {
			return nil, errWrongType
		}
	} else if strings.HasPrefix(volume, `\\?\`) && (len(volume) != 6 || volume[5] != ':') {
		return nil, errWrongType
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errWrongType
	}
	info, err = os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errWrongType
	}
	// Префикс сохраняет поддержку длинных путей для низкоуровневого API.
	switch {
	case strings.HasPrefix(path, `\\?\`):
	case strings.HasPrefix(path, `\\`):
		path = `\\?\UNC\` + path[2:]
	default:
		path = `\\?\` + path
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), deckJSON)
	if err := checkHandle(fd); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func checkHandle(fd syscall.Handle) error {
	kind, err := syscall.GetFileType(fd)
	if err != nil {
		return err
	}
	if kind != syscall.FILE_TYPE_DISK {
		return errWrongType
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(fd, &info); err != nil {
		return err
	}
	if info.FileAttributes&(syscall.FILE_ATTRIBUTE_DIRECTORY|syscall.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return errWrongType
	}
	return nil
}
