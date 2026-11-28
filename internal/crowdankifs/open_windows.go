package crowdankifs

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func openRegular(dir string) (*os.File, error) {
	// Соединение без Clean: `..` разрешает ОС, в том числе после ссылки.
	dirPath := strings.ReplaceAll(strings.TrimRight(dir, `\/`), "/", "\\")
	path := dirPath + `\` + deckJSON
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
	// Проверка метаданных и открытие получают одну и ту же строку: иначе
	// Win32 нормализует имя при проверке и не нормализует при открытии.
	info, err := os.Stat(forOpen(dirPath))
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errWrongType
	}
	path = forOpen(path)
	info, err = os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errWrongType
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

// forOpen выбирает extended-length форму только там, где без неё не работает
// низкоуровневый вызов: у префикса отключена нормализация Win32, поэтому
// короткий путь передаётся как есть.
func forOpen(path string) string {
	switch {
	case strings.HasPrefix(path, `\\?\`), strings.HasPrefix(path, `\??\`), len(path) < 248:
		return path
	case strings.HasPrefix(path, `\\`):
		return `\\?\UNC\` + path[2:]
	default:
		return `\\?\` + path
	}
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
