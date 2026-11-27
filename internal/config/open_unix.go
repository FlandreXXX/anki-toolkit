//go:build unix

package config

import (
	"os"
	"syscall"
)

// openConfig открывает файл конфигурации без ожидания на FIFO: O_NONBLOCK
// снимает блокировку открытия специального объекта, а пригодность объекта
// проверяется по дескриптору в consumeConfig.
func openConfig(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
