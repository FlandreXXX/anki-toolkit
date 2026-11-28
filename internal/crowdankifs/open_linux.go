package crowdankifs

import (
	"errors"
	"os"
	"strconv"
	"syscall"
)

// Флаг Linux доступен в syscall не на всех архитектурах.
const oPath = 0x200000

func openRegular(dir string) (*os.File, error) {
	root, err := syscall.Open(dir, oPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOTDIR) {
		return nil, errWrongType
	}
	if err != nil {
		return nil, err
	}
	defer syscall.Close(root)

	// O_PATH не открывает специальный объект для чтения. O_NOFOLLOW
	// оставляет конечную ссылку ссылкой для проверки по дескриптору.
	fd, err := syscall.Openat(root, deckJSON, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	meta := os.NewFile(uintptr(fd), deckJSON)
	defer meta.Close()
	want, err := meta.Stat()
	if err != nil {
		return nil, err
	}
	if !want.Mode().IsRegular() {
		return nil, errWrongType
	}

	// procfs даёт доступ к тому же объекту после проверки, даже если
	// запись deck.json уже заменили. O_NONBLOCK защищает открытие FIFO.
	file, err := os.OpenFile("/proc/self/fd/"+strconv.FormatUint(uint64(meta.Fd()), 10), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		// Отсутствие procfs относится к среде, а не к пути экспорта.
		return nil, errors.Join(errUnavailable, err)
	}
	got, err := file.Stat()
	if err != nil || !got.Mode().IsRegular() || !os.SameFile(want, got) {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errWrongType
	}
	return file, nil
}
