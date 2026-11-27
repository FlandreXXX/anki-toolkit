package main_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// readFailureCases готовит нативный отказ: файл открыт с разделением доступа,
// запрещающим параллельное чтение. Второе открытие того же файла получает
// ERROR_SHARING_VIOLATION, поэтому сценарий не зависит от прав и uid.
func readFailureCases(t *testing.T, base string) []readFailureCase {
	t.Helper()
	path := filepath.Join(base, "занятая конфигурация.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("Открытие с ограничением совместного доступа: %v", err)
	}
	t.Cleanup(func() {
		if err := syscall.CloseHandle(handle); err != nil && !errors.Is(err, syscall.EINVAL) {
			t.Errorf("Закрытие дескриптора: %v", err)
		}
	})
	// CreateFile без FILE_SHARE_* отвергает второе открытие детерминированно,
	// поэтому успешное чтение означает поломку предположения теста, а не
	// особенность среды: молча пропускать обязательный сценарий нельзя.
	if _, err := os.ReadFile(path); err == nil {
		t.Fatalf("Файл %q открыт повторно, хотя дескриптор держит его без разделения доступа: ERROR_SHARING_VIOLATION не наблюдается.", path)
	}
	return []readFailureCase{{"занятый файл конфигурации", path}}
}
