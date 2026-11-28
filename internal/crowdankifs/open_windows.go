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
	// Проверки и открытие идут по одной строке: иначе Win32 нормализует имя
	// на одном шаге и не нормализует на другом.
	path, err := forOpen(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(parent(path))
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

// parent отрезает имя файла, не трогая остальное.
func parent(path string) string {
	if i := strings.LastIndexByte(path, '\\'); i > 0 {
		return path[:i]
	}
	return path
}

// forOpen переводит путь в форму, которой нужен extended-length префикс, только
// для длинных путей. У префикса отключена нормализация Win32, поэтому перед ним
// путь разрешается — иначе `..` после ссылки дал бы другой каталог, чем при
// проверке.
func forOpen(path string) (string, error) {
	switch {
	case strings.HasPrefix(path, `\\?\`), strings.HasPrefix(path, `\??\`), len(path) < 248:
		return path, nil
	}
	full, err := syscall.FullPath(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(full, `\\`) {
		return `\\?\UNC\` + full[2:], nil
	}
	return `\\?\` + full, nil
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
