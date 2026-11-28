//go:build !unix

package config

import "os"

// openConfig открывает файл конфигурации средствами платформы. Unix-флаги
// не применяются; windows, plan9, js и wasip1 проверяют тип по дескриптору.
func openConfig(path string) (*os.File, error) {
	return os.Open(path)
}
