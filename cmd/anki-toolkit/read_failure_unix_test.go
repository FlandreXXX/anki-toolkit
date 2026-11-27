//go:build !windows

package main_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// readFailureCases готовит контролируемые системные отказы чтения конфигурации.
// Цикл символических ссылок даёт ELOOP и не зависит от uid, поэтому подходит и
// под root. Запрет доступа проверяется только без root: chmod 000 действует лишь
// на непривилегированного пользователя.
func readFailureCases(t *testing.T, base string) []readFailureCase {
	t.Helper()
	cases := []readFailureCase{}
	loopA, loopB := filepath.Join(base, "цикл-a"), filepath.Join(base, "цикл-b")
	for _, link := range [][2]string{{loopA, loopB}, {loopB, loopA}} {
		if err := os.Symlink(link[1], link[0]); err != nil {
			t.Fatalf("Символическая ссылка %q: %v", link[0], err)
		}
	}
	cases = append(cases, readFailureCase{"цикл символических ссылок", loopA})

	if os.Geteuid() == 0 {
		t.Log("Процесс запущен от root: подслучай chmod 000 пропущен, он не ограничивает доступ.")
		return cases
	}
	denied := filepath.Join(base, "закрытая конфигурация.json")
	if err := os.WriteFile(denied, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(denied, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(denied, 0o600) })
	if _, err := os.ReadFile(denied); err == nil {
		t.Logf("%s: запрет доступа не наблюдается (root=%t), подслучай пропущен", runtime.GOOS, os.Geteuid() == 0)
		return cases
	}
	return append(cases, readFailureCase{"файл без права чтения", denied})
}
