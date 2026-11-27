package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// contractHeaders — независимый эталон контракта: пять обязательных заголовков
// в требуемом относительном порядке. Зафиксирован здесь буквально, а не выведен
// из производственной переменной requiredSections, поэтому изменение состава
// или порядка в реализации не подменяет тестовый вход молча.
var contractHeaders = []string{"Цель", "Изменения", "Проверки", "CI", "Ограничения"}

// sectionTexts — содержательный текст раздела для синтетических входов.
var sectionTexts = map[string]string{
	"Цель":        "Сделать проверку описания PR предсказуемой.",
	"Изменения":   "Добавлен валидатор описания и тонкий CI-адаптер.",
	"Проверки":    "go test и go vet выполнены локально.",
	"CI":          "Запуск ещё не завершён, отметка pending.",
	"Ограничения": "Существенных ограничений нет.",
}

// bodyFromOrder собирает описание из заголовков в заданном порядке.
func bodyFromOrder(order []string) string {
	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, "## "+name+"\n"+sectionTexts[name])
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// bodyWith собирает полное описание контракта с заменённым текстом разделов.
func bodyWith(overrides map[string]string) string {
	texts := maps.Clone(sectionTexts)
	maps.Copy(texts, overrides)
	parts := make([]string, 0, len(contractHeaders))
	for _, name := range contractHeaders {
		parts = append(parts, "## "+name+"\n"+texts[name])
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func validBody() string { return bodyFromOrder(contractHeaders) }

// bodyWithout собирает описание без одного обязательного раздела.
func bodyWithout(name string) string {
	order := make([]string, 0, len(contractHeaders))
	for _, header := range contractHeaders {
		if header != name {
			order = append(order, header)
		}
	}
	return bodyFromOrder(order)
}

// missingHeaders — ожидаемая диагностика при полном отсутствии разделов.
func missingHeaders() []string {
	problems := make([]string, 0, len(contractHeaders))
	for _, name := range contractHeaders {
		problems = append(problems, "Отсутствует обязательный заголовок: ## "+name)
	}
	return problems
}

// TestContractReferenceIsIndependent фиксирует пять обязательных заголовков и их
// порядок как внешний контракт тестов: изменение производственной переменной не
// должно проходить незамеченным.
func TestContractReferenceIsIndependent(t *testing.T) {
	if !slices.Equal(requiredSections, contractHeaders) {
		t.Fatalf("контракт разделов изменился: %q, эталон тестов %q", requiredSections, contractHeaders)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{name: "все разделы с текстом", body: validBody()},
		{
			name: "дополнительный раздел между обязательными",
			body: strings.Replace(validBody(), "## Проверки",
				"## Безопасность\nСекреты не добавлены.\n\n## Проверки", 1),
		},
		{
			name: "отсутствующий раздел",
			body: bodyWithout("CI"),
			want: []string{"Отсутствует обязательный заголовок: ## CI"},
		},
		{
			name: "повторяющийся раздел",
			body: validBody() + "\n## Цель\nПовторный раздел.\n",
			want: []string{"Заголовок повторяется: ## Цель"},
		},
		{
			name: "перестановка первых двух разделов",
			body: bodyFromOrder([]string{"Изменения", "Цель", "Проверки", "CI", "Ограничения"}),
			want: []string{"Нарушен порядок разделов: ## Изменения расположен раньше, чем ## Цель"},
		},
		{
			name: "перестановка средних разделов",
			body: bodyFromOrder([]string{"Цель", "Изменения", "CI", "Проверки", "Ограничения"}),
			want: []string{"Нарушен порядок разделов: ## CI расположен раньше, чем ## Проверки"},
		},
		{
			name: "перестановка последних разделов",
			body: bodyFromOrder([]string{"Цель", "Изменения", "Проверки", "Ограничения", "CI"}),
			want: []string{"Нарушен порядок разделов: ## Ограничения расположен раньше, чем ## CI"},
		},
		{
			name: "раздел только в комментарии",
			body: "<!--\n" + validBody() + "\n-->\n",
			want: missingHeaders(),
		},
		{
			name: "незакрытый комментарий скрывает остаток",
			body: "## Цель\nИнформация.\n<!--\n## Изменения\nИнформация.\n## Проверки\nИнформация.\n",
			want: []string{
				"Отсутствует обязательный заголовок: ## Изменения",
				"Отсутствует обязательный заголовок: ## Проверки",
				"Отсутствует обязательный заголовок: ## CI",
				"Отсутствует обязательный заголовок: ## Ограничения",
			},
		},
		{
			name: "заголовки в ограждённом блоке",
			body: "```md\n" + validBody() + "```\n",
			want: missingHeaders(),
		},
		{
			name: "тильда-ограждение",
			body: "~~~md\n" + validBody() + "~~~\n",
			want: missingHeaders(),
		},
		{
			name: "строка информации о блоке не закрывает его",
			body: "```text\n```md\n" + validBody() + "```\n",
			want: missingHeaders(),
		},
		{
			name: "более длинный маркер не закрывается коротким",
			body: "````md\n```\n" + validBody() + "````\n",
			want: missingHeaders(),
		},
		{
			name: "разные маркеры не закрывают друг друга",
			body: "~~~md\n" + validBody() + "```\n",
			want: missingHeaders(),
		},
		{
			name: "незакрытый блок верхнего уровня скрывает остаток",
			body: "```md\n" + validBody(),
			want: missingHeaders(),
		},
		{
			name: "заголовок в блок-цитате",
			body: "> ## Цель\n> Информация.\n" + bodyWithout("Цель"),
			want: []string{"Отсутствует обязательный заголовок: ## Цель"},
		},
		{
			name: "заголовок с отступом кода",
			body: "    ## Цель\n    Информация.\n" + bodyWithout("Цель"),
			want: []string{"Отсутствует обязательный заголовок: ## Цель"},
		},
		{
			name: "заголовок с отступом табуляции",
			body: "\t## Цель\n\tИнформация.\n" + bodyWithout("Цель"),
			want: []string{"Отсутствует обязательный заголовок: ## Цель"},
		},
		{
			name: "корректные закрывающие решётки не входят в имя",
			body: strings.Replace(validBody(), "## Цель", "## Цель ###", 1),
		},
		{
			name: "заголовки первого и третьего уровня не считаются разделами",
			body: "# Цель\nИнформация.\n\n### Изменения\nИнформация.\n\n" + validBody(),
		},
		{
			name: "пустой раздел",
			body: strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "   ", 1),
			want: []string{"Пустой обязательный раздел: ## CI"},
		},
		{
			name: "раздел только из горизонтальной линии",
			body: strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "---", 1),
			want: []string{"Пустой обязательный раздел: ## CI"},
		},
		{
			name: "раздел только из блока кода",
			body: strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "```\nэто пример\n```", 1),
			want: []string{"Пустой обязательный раздел: ## CI"},
		},
		{
			name: "заглушка TODO",
			body: strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "TODO", 1),
			want: []string{"Раздел содержит только заглушку: ## CI"},
		},
		{
			name: "заглушка с многоточием",
			body: strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "…", 1),
			want: []string{"Раздел содержит только заглушку: ## CI"},
		},
		{
			name: "заглушка из шаблонной подсказки",
			body: strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "<статус CI>", 1),
			want: []string{"Раздел содержит только заглушку: ## CI"},
		},
		{
			name: "несколько заглушек вместо текста",
			body: strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "TODO\nTBD\n—", 1),
			want: []string{"Раздел содержит только заглушку: ## CI"},
		},
		{
			name: "содержательная фраза об отсутствии ограничений принимается",
			body: bodyWith(map[string]string{"Ограничения": "Существенных ограничений нет."}),
		},
		{
			name: "честный статус невыполненной проверки принимается",
			body: bodyWith(map[string]string{"CI": "not run: workflow ещё не добавлен в base."}),
		},
		{
			name: "комментарий рядом с текстом не скрывает текст",
			body: bodyWith(map[string]string{"CI": "<!-- пояснение -->\nФакт CI."}),
		},
		{
			name: "маркер комментария внутри ограждённого блока литерален",
			body: validBody() + "\n```md\n<!-- скрытый пример -->\n```\n",
		},
		{
			name: "пустое описание",
			body: "\n \n",
			want: []string{
				"Описание PR пустое: нужны обязательные разделы " +
					"## Цель, ## Изменения, ## Проверки, ## CI, ## Ограничения",
			},
		},
		{
			name: "строка null не является содержанием",
			body: "null\n",
			want: missingHeaders(),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validate(c.body)
			if !slices.Equal(got, c.want) {
				t.Fatalf("нарушения %q, ожидались %q", got, c.want)
			}
		})
	}
}

// TestInlineCodeCommentMarkerIsLiteral — регрессия: литеральный маркер `<!--`
// в строчном коде не открывает HTML-комментарий и не скрывает следующие
// заголовки. Воспроизводится на полном описании с единственным отклонением.
func TestInlineCodeCommentMarkerIsLiteral(t *testing.T) {
	body := bodyWith(map[string]string{
		"Изменения": "Добавить обработку маркера `<!--` в Markdown и проверить его.",
	})
	if got := validate(body); len(got) != 0 {
		t.Fatalf("маркер в строчном коде скрыл описание: %q", got)
	}
	// Двойные обратные кавычки — тоже строчный код.
	body = bodyWith(map[string]string{
		"Изменения": "Пример ``<!--`` внутри строчного кода описания.",
	})
	if got := validate(body); len(got) != 0 {
		t.Fatalf("маркер в строчном коде из двух кавычек скрыл описание: %q", got)
	}
	// Настоящий комментарий рядом со строчным кодом по-прежнему скрывает текст.
	body = bodyWith(map[string]string{
		"CI": "Код `<!--` виден.\n<!-- скрытый фрагмент -->",
	})
	if got := validate(body); len(got) != 0 {
		t.Fatalf("комментарий рядом со строчным кодом отклонён: %q", got)
	}
	// Незакрытый настоящий комментарий по-прежнему скрывает всё до конца,
	// но видимый текст самого раздела сохраняется.
	body = bodyWith(map[string]string{
		"CI": "Код `<!--` виден.\n<!-- незакрытый комментарий",
	})
	want := []string{"Отсутствует обязательный заголовок: ## Ограничения"}
	if got := validate(body); !slices.Equal(got, want) {
		t.Fatalf("нарушения %q, ожидались %q", got, want)
	}
}

// TestIndentedCodeCommentMarkerIsLiteral — регрессия: маркер `<!--` в настоящем
// блоке кода с отступом не изменяет состояние комментария.
func TestIndentedCodeCommentMarkerIsLiteral(t *testing.T) {
	body := bodyWith(map[string]string{
		"Изменения": "Пример разметки:\n\n    <!-- литеральный маркер в коде\n\nПосле примера обычный текст.",
	})
	if got := validate(body); len(got) != 0 {
		t.Fatalf("маркер в блоке кода с отступом скрыл описание: %q", got)
	}
	// Незакрытый настоящий комментарий с отступом внутри по-прежнему закрывается
	// собственным маркером и не ломает семантику комментария.
	body = bodyWith(map[string]string{
		"CI": "Факт CI.\n<!--\n    текст внутри комментария\n-->\nВторой факт CI.",
	})
	if got := validate(body); len(got) != 0 {
		t.Fatalf("блок кода внутри комментария отклонён: %q", got)
	}
}

// TestListFence — ограждённый блок внутри одноуровневого пункта списка в обеих
// формах открытия: в строке пункта и на последующей строке с отступом.
func TestListFence(t *testing.T) {
	t.Run("заголовки внутри блока пункта не засчитываются", func(t *testing.T) {
		body := bodyWith(map[string]string{
			"Цель": "- Пример разметки.\n\n  ```text\n  ## Изменения\n  ## Проверки\n  ```",
		})
		if got := validate(body); len(got) != 0 {
			t.Fatalf("заголовки внутри блока пункта засчитаны или блок сломал разбор: %q", got)
		}
	})
	t.Run("заголовок после выхода из пункта распознаётся", func(t *testing.T) {
		body := bodyWith(map[string]string{
			"Цель": "- Пояснение к примеру.\n\n  ```text\n  Пример кода без закрывающего маркера.",
		})
		if got := validate(body); len(got) != 0 {
			t.Fatalf("незакрытый блок пункта скрыл последующие разделы: %q", got)
		}
	})
	t.Run("следующий пункт списка остаётся списком", func(t *testing.T) {
		body := bodyWith(map[string]string{
			"Изменения": "- первый пункт\n- второй пункт\n\n  ```text\n  пример\n  ```\n\n- третий пункт",
		})
		if got := validate(body); len(got) != 0 {
			t.Fatalf("список после блока отклонён: %q", got)
		}
	})
	t.Run("блок верхнего уровня с отступом остаётся блоком", func(t *testing.T) {
		// Отступ меньше начала содержимого пункта: это блок верхнего уровня.
		body := bodyWith(map[string]string{
			"Изменения": "Текст пункта.\n\n- пункт списка\n\n ```text\n ## Изменения\n ```\n\nПродолжение.",
		})
		if got := validate(body); len(got) != 0 {
			t.Fatalf("блок верхнего уровня сломал разбор: %q", got)
		}
	})
	t.Run("блок открыт в строке пункта", func(t *testing.T) {
		lines := strings.Split(validBody(), "\n")
		inner := make([]string, len(lines))
		for i, line := range lines {
			inner[i] = "  " + line
		}
		body := "- ```markdown\n" + strings.Join(inner, "\n") + "\n  ```\n"
		if got := validate(body); !slices.Equal(got, missingHeaders()) {
			t.Fatalf("нарушения %q, ожидались %q", got, missingHeaders())
		}
	})
	// Открывающий маркер — на отдельной строке после пункта списка, с отступом
	// от начала его содержимого; закрывающий маркер согласован с открывающим.
	for _, marker := range []string{"- ", "1. "} {
		for _, indent := range []string{"  ", "   ", "\t"} {
			t.Run("блок открыт на отдельной строке "+marker+indent, func(t *testing.T) {
				lines := strings.Split(validBody(), "\n")
				inner := make([]string, len(lines))
				for i, line := range lines {
					inner[i] = indent + line
				}
				body := marker + "Пункт списка.\n\n" + indent + "```md\n" +
					strings.Join(inner, "\n") + "\n" + indent + "```\n"
				if got := validate(body); !slices.Equal(got, missingHeaders()) {
					t.Fatalf("нарушения %q, ожидались %q", got, missingHeaders())
				}
			})
		}
	}
	// Ограждение с отступом от начала содержимого пункта — тоже блок, а не текст:
	// содержимое внутри него не засчитывается разделами.
	t.Run("ограждение нумерованного пункта с отступом четыре", func(t *testing.T) {
		lines := strings.Split(validBody(), "\n")
		inner := make([]string, len(lines))
		for i, line := range lines {
			inner[i] = "    " + line
		}
		body := "10. TODO\n\n    ```text\n" + strings.Join(inner, "\n") + "\n    ```\n"
		if got := validate(body); !slices.Equal(got, missingHeaders()) {
			t.Fatalf("нарушения %q, ожидались %q", got, missingHeaders())
		}
	})
	// Блок под пунктом прикрывает единственную заглушку: раздел остаётся заглушкой,
	// а не становится содержательным из-за текста внутри блока.
	t.Run("блок не делает заглушку содержанием", func(t *testing.T) {
		body := bodyWith(map[string]string{
			"Проверки": "10. TODO\n\n    ```text\n    Пример команды.\n    ```",
		})
		want := []string{"Раздел содержит только заглушку: ## Проверки"}
		if got := validate(body); !slices.Equal(got, want) {
			t.Fatalf("нарушения %q, ожидались %q", got, want)
		}
	})
}

// TestListIndentedContinuation — регрессия: отступ продолжения пункта списка
// отсчитывается от начала его содержимого, а не от нуля. Четыре пробела под
// пунктом списка — продолжение пункта, а не блок кода, поэтому текст остаётся
// содержанием раздела.
//
// Вход намеренно содержит только пустой маркер в первой строке: содержательный
// текст лежит в продолжении, поэтому при ошибочном отбрасывании продолжения
// раздел становится заглушкой и тест падает.
func TestListIndentedContinuation(t *testing.T) {
	for name, text := range map[string]string{
		"продолжение под пунктом с отметкой":  "- [x]\n    go test выполнен.",
		"продолжение под пустым маркером":     "- \n    go test выполнен.",
		"продолжение под нумерованным":        "1. \n    go test выполнен.",
		"продолжение под пунктом в 2 колонки": "- пункт\n\tgo test выполнен.",
	} {
		t.Run(name, func(t *testing.T) {
			got := validate(bodyWith(map[string]string{"Проверки": text}))
			if len(got) != 0 {
				t.Fatalf("продолжение пункта отклонено как заглушка: %q", got)
			}
		})
	}
	// Настоящий блок кода с отступом вне списка по-прежнему скрывает текст.
	if got := validate(bodyWith(map[string]string{
		"Проверки": "    go test выполнен.",
	})); !slices.Equal(got, []string{"Пустой обязательный раздел: ## Проверки"}) {
		t.Fatalf("блок кода вне списка засчитан содержанием: %q", got)
	}
	// Отметка без продолжения остаётся заглушкой.
	if got := validate(bodyWith(map[string]string{
		"Проверки": "- [x]",
	})); !slices.Equal(got, []string{"Раздел содержит только заглушку: ## Проверки"}) {
		t.Fatalf("пустая отметка принята содержанием: %q", got)
	}
}

// TestFenceInfoStringMarkerIsLiteral — регрессия: маркер `<!--` в строке
// информации ограждения не открывает HTML-комментарий, иначе последующие
// заголовки ошибочно скрываются.
func TestFenceInfoStringMarkerIsLiteral(t *testing.T) {
	body := "```text <!--\nпример\n```\n\n" + validBody()
	if got := validate(body); len(got) != 0 {
		t.Fatalf("маркер в строке информации скрыл описание: %q", got)
	}
	// Ограждённый блок внутри настоящего комментария остаётся скрытым текстом:
	// комментарий закрывается своим маркером, а не ограждением.
	body = bodyWith(map[string]string{
		"CI": "Факт CI.\n<!-- скрытый блок\n```text\nпример\n```\n-->\nВторой факт CI.",
	})
	if got := validate(body); len(got) != 0 {
		t.Fatalf("комментарий вокруг ограждения отклонён: %q", got)
	}
}

// TestListPlaceholders — маркеры маркированного, нумерованного списка и списка
// задач не являются содержанием: раздел из одних заглушек отклоняется, список с
// реальным текстом принимается.
func TestListPlaceholders(t *testing.T) {
	placeholders := map[string]string{
		"- TODO":                "маркированный пункт",
		"1. Заполнить":          "нумерованный пункт",
		"2) Уточнить":           "нумерованный пункт с круглой скобкой",
		"- [ ] Уточнить":        "пункт списка задач без отметки",
		"- [x] TBD":             "пункт списка задач с отметкой",
		"* —":                   "маркированный пункт с тире",
		"1. TODO\n2. Заполнить": "несколько заглушек подряд",
		"- [ ]":                 "пустая отметка списка задач",
		"-":                     "пустой маркированный пункт",
		"*":                     "пустой маркированный пункт со звёздочкой",
		"1.":                    "пустой нумерованный пункт",
		"1)":                    "пустой нумерованный пункт с круглой скобкой",
		"10.":                   "пустой нумерованный пункт из двух цифр",
	}
	for text, name := range placeholders {
		t.Run(name, func(t *testing.T) {
			got := validate(bodyWith(map[string]string{"Ограничения": text}))
			want := []string{"Раздел содержит только заглушку: ## Ограничения"}
			if !slices.Equal(got, want) {
				t.Fatalf("нарушения %q, ожидались %q", got, want)
			}
		})
	}
	for name, text := range map[string]string{
		"список с содержательным текстом":    "- валидатор проверяет пять разделов\n- второй пункт списка",
		"заглушка вместе с реальным текстом": "- TODO\n- проверка выполнена локально",
		"нумерованный список с текстом":      "1. Проверка выполнена.\n2. Результат записан.",
		"число с текстом":                    "2024 год, проверка выполнена.",
		"десятичная дробь":                   "1.5 млн запросов обработано.",
		"дефис без пробела":                  "-Да, проверено.",
		"сноска со звёздочкой":               "* сноска с пояснением",
	} {
		t.Run(name, func(t *testing.T) {
			if got := validate(bodyWith(map[string]string{"Ограничения": text})); len(got) != 0 {
				t.Fatalf("содержательный список отклонён: %q", got)
			}
		})
	}
}

// TestHardWrapRejected — регрессия: абзац, разбитый жёстким переносом по ширине
// колонки, отклоняется. Такой текст склеивается при рендеринге и превращается в
// рваный; каждая строка абзаца пишется целиком.
func TestHardWrapRejected(t *testing.T) {
	long1 := "Абзац описания, намеренно разбитый на строки по ширине терминала,"
	long2 := "продолжает предложение на следующей строке без пустой между ними."
	body := bodyWith(map[string]string{"Цель": long1 + "\n" + long2})
	want := []string{
		"Жёсткий перенос строки в разделе ## Цель: абзац пишется одной " +
			"строкой, отдельные абзацы разделяются пустой строкой",
	}
	if got := validate(body); !slices.Equal(got, want) {
		t.Fatalf("нарушения %q, ожидались %q", got, want)
	}
	// Тот же текст, разделённый пустой строкой, — два законных абзаца.
	if got := validate(bodyWith(map[string]string{"Цель": long1 + "\n\n" + long2})); len(got) != 0 {
		t.Fatalf("раздельные абзацы отклонены: %q", got)
	}
	// Соседние короткие строки переносом не считаются: это разные абзацы.
	if got := validate(bodyWith(map[string]string{"Цель": "Строка.\nЕщё строка."})); len(got) != 0 {
		t.Fatalf("короткие строки приняты за перенос: %q", got)
	}
	// Пункты списка и строки таблицы занимают собственную строку законно.
	if got := validate(bodyWith(map[string]string{
		"Изменения": "- " + long1 + "\n- " + long2,
	})); len(got) != 0 {
		t.Fatalf("список отклонён как перенос: %q", got)
	}
	if got := validate(bodyWith(map[string]string{
		"Проверки": "| Проверка | Результат |\n| --- | --- |\n| тесты | ok |",
	})); len(got) != 0 {
		t.Fatalf("таблица отклонена как перенос: %q", got)
	}
}

func TestCodePointLimit(t *testing.T) {
	base := validBody()
	if count := utf8.RuneCountInString(base); count >= maxBodyPoints {
		t.Fatalf("базовое описание уже длиннее лимита: %d", count)
	}
	exact := base + strings.Repeat("😀", maxBodyPoints-utf8.RuneCountInString(base))
	if got := validate(exact); len(got) != 0 {
		t.Fatalf("ровно %d кодовых точек отклонены: %q", maxBodyPoints, got)
	}
	if got := validate(exact + "😀"); len(got) == 0 {
		t.Fatalf("превышение лимита на одну кодовую точку принято")
	} else if !strings.Contains(strings.Join(got, "\n"), "10001") {
		t.Fatalf("диагностика лимита без фактического числа: %q", got)
	}
	// Кириллица: байты превышают лимит, кодовые точки — нет.
	cyrillic := base + strings.Repeat("я", maxBodyPoints-utf8.RuneCountInString(base))
	if len(cyrillic) <= maxBodyPoints {
		t.Fatalf("ожидался вход длиннее лимита в байтах: %d", len(cyrillic))
	}
	if got := validate(cyrillic); len(got) != 0 {
		t.Fatalf("кириллица у границы отклонена: %q", got)
	}
}

func TestLineEndingsNormalized(t *testing.T) {
	base := validBody()
	for name, body := range map[string]string{
		"crlf": strings.ReplaceAll(base, "\n", "\r\n"),
		"cr":   strings.ReplaceAll(base, "\n", "\r"),
	} {
		t.Run(name, func(t *testing.T) {
			if got := validate(body); len(got) != 0 {
				t.Fatalf("окончания строк не нормализованы: %q", got)
			}
		})
	}
	// CRLF не должен съедать одну кодовую точку из лимита.
	exact := base + strings.Repeat("😀", maxBodyPoints-utf8.RuneCountInString(base))
	crlf := strings.ReplaceAll(exact, "\n", "\r\n")
	if got := validate(crlf); len(got) != 0 {
		t.Fatalf("CRLF сдвинул границу лимита: %q", got)
	}
	if got := validate(crlf + "😀"); len(got) == 0 {
		t.Fatalf("граница после нормализации CRLF не проверяется")
	}
}

func TestShellMetacharactersAreData(t *testing.T) {
	text := "Оболочка не вызывается: | $(id) `whoami` ; rm -rf / не выполняются."
	body := bodyWith(map[string]string{
		"Проверки": text + "\n\n```sh\n<!-- не комментарий, а литерал -->\n$ echo $(id)\n```",
	})
	if got := validate(body); len(got) != 0 {
		t.Fatalf("разметка в тексте обработана как код: %q", got)
	}
	// Полное недоверенное описание не попадает в вывод.
	problems := validate(strings.Replace(body, "## Цель", "## Цель $(id) |", 1))
	for _, problem := range problems {
		if strings.Contains(problem, "$(id)") || strings.Contains(problem, "whoami") {
			t.Fatalf("диагностика воспроизводит описание: %q", problem)
		}
	}
}

// TestValidatePlaceholderRejected доказывает, что чистая функция отклоняет
// заглушку в обязательном разделе с указанием именно этого раздела.
func TestValidatePlaceholderRejected(t *testing.T) {
	for name, text := range map[string]string{
		"todo":      "TODO",
		"tbd":       "TBD",
		"заполнить": "заполнить",
		"уточнить":  "уточнить",
		"точки":     "...",
		"тире":      "—",
		"дефис":     "-",
		"нет":       "нет",
		"n/a":       "n/a",
		"вопрос":    "?",
	} {
		t.Run(name, func(t *testing.T) {
			got := validate(bodyWith(map[string]string{"Ограничения": text}))
			want := []string{"Раздел содержит только заглушку: ## Ограничения"}
			if !slices.Equal(got, want) {
				t.Fatalf("нарушения %q, ожидались %q", got, want)
			}
		})
	}
}

// writeTemp создаёт файл во временном каталоге теста и возвращает путь.
func writeTemp(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("не удалось создать %s: %v", name, err)
	}
	return path
}

func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestUsageErrors(t *testing.T) {
	body := writeTemp(t, "body.md", []byte(validBody()))
	event := writeTemp(t, "event.json", []byte(`{"pull_request":{"body":"## Цель\nТекст."}}`))
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "без режима", args: nil, wantErr: "ровно один режим"},
		{name: "оба режима", args: []string{"--body-file", body, "--event-file", event}, wantErr: "взаимоисключающие"},
		{name: "оба режима, пустой путь события", args: []string{"--body-file", body, "--event-file", ""}, wantErr: "взаимоисключающие"},
		{name: "оба режима, пустой путь описания", args: []string{"--body-file", "", "--event-file", event}, wantErr: "взаимоисключающие"},
		{name: "неизвестный флаг", args: []string{"--bogus", "x"}, wantErr: "Ошибка использования"},
		{name: "лишний позиционный аргумент", args: []string{"--body-file", body, "лишнее"}, wantErr: "лишние позиционные"},
		{name: "флаг без значения", args: []string{"--body-file"}, wantErr: "Ошибка использования"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(c.args...)
			if code != 2 {
				t.Fatalf("код выхода %d, ожидался 2", code)
			}
			if !strings.Contains(stderr, c.wantErr) {
				t.Fatalf("в stderr нет %q: %q", c.wantErr, stderr)
			}
			if stdout != "" {
				t.Fatalf("неожиданный вывод в stdout: %q", stdout)
			}
		})
	}
}

func TestInputErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "нет-такого-файла.md")
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "отсутствующий файл описания", args: []string{"--body-file", missing}, wantErr: "Ошибка входа"},
		{name: "нечитаемый файл события", args: []string{"--event-file", missing}, wantErr: "Ошибка входа"},
		{name: "некорректный UTF-8 в описании", args: []string{"--body-file", writeTemp(t, "bad.md", []byte{0xff, 0xfe, 0x20})}, wantErr: "UTF-8"},
		{name: "некорректный UTF-8 в событии", args: []string{"--event-file", writeTemp(t, "bad.json", []byte{0xff, 0x00})}, wantErr: "UTF-8"},
		{name: "некорректный JSON", args: []string{"--event-file", writeTemp(t, "broken.json", []byte(`{"pull_request":`))}, wantErr: "JSON"},
		{name: "нет объекта pull_request", args: []string{"--event-file", writeTemp(t, "no-pr.json", []byte(`{"action":"edited"}`))}, wantErr: "pull_request"},
		{name: "pull_request null", args: []string{"--event-file", writeTemp(t, "null-pr.json", []byte(`{"pull_request":null}`))}, wantErr: "pull_request"},
		{name: "pull_request не объект", args: []string{"--event-file", writeTemp(t, "array-pr.json", []byte(`{"pull_request":[]}`))}, wantErr: "не является объектом"},
		{name: "описание не строка", args: []string{"--event-file", writeTemp(t, "num-body.json", []byte(`{"pull_request":{"body":42}}`))}, wantErr: "строкой"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _, stderr := runCLI(c.args...)
			if code != 2 {
				t.Fatalf("код выхода %d, ожидался 2: %q", code, stderr)
			}
			if !strings.Contains(stderr, c.wantErr) {
				t.Fatalf("в stderr нет %q: %q", c.wantErr, stderr)
			}
		})
	}
}

func TestManualEmptyBodyIsViolation(t *testing.T) {
	for name, payload := range map[string]string{
		"null":          `{"pull_request":{"body":null,"user":{"login":"someone","type":"User"},"head":{"ref":"feat/x"}}}`,
		"пустая строка": `{"pull_request":{"body":"","user":{"login":"someone","type":"User"},"head":{"ref":"feat/x"}}}`,
		"пробелы":       `{"pull_request":{"body":"  \n\t\n","user":{"login":"someone","type":"User"},"head":{"ref":"feat/x"}}}`,
		"нет поля":      `{"pull_request":{"user":{"login":"someone","type":"User"},"head":{"ref":"feat/x"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeTemp(t, "event.json", []byte(payload))
			code, stdout, stderr := runCLI("--event-file", path)
			if code != 1 {
				t.Fatalf("код выхода %d, ожидался 1", code)
			}
			if !strings.Contains(stderr, "Описание PR пустое") {
				t.Fatalf("нет диагностики пустого описания: %q", stderr)
			}
			if stdout != "" {
				t.Fatalf("неожиданный вывод в stdout: %q", stdout)
			}
		})
	}
}

// eventJSON собирает синтетический снимок события pull_request из готового
// объекта PR; sender всегда бот, поэтому проверяется защита от подмены.
func eventJSON(t *testing.T, pr map[string]any) string {
	t.Helper()
	pr["title"] = "renovate[bot] renovate/update"
	pr["labels"] = []map[string]any{{"name": "dependencies"}}
	payload, err := json.Marshal(map[string]any{
		"action":       "synchronize",
		"sender":       map[string]any{"login": "renovate[bot]", "type": "Bot"},
		"pull_request": pr,
	})
	if err != nil {
		t.Fatalf("не удалось собрать событие: %v", err)
	}
	return string(payload)
}

// eventWithUser — событие с указанными учётной записью автора и веткой.
func eventWithUser(t *testing.T, body, login, accountType, branch string) string {
	return eventJSON(t, map[string]any{
		"body": body,
		"user": map[string]any{"login": login, "type": accountType},
		"head": map[string]any{"ref": branch},
	})
}

func TestBotExemption(t *testing.T) {
	// Исключение проверяется на заведомо непригодном описании: успех возможен
	// только за счёт сработавшего исключения.
	bad := badBody()
	cases := []struct {
		name        string
		payload     func(t *testing.T) string
		wantCode    int
		wantExempt  bool
		wantProblem string
	}{
		{
			name: "бот обновления зависимостей с префиксом",
			payload: func(t *testing.T) string {
				return eventWithUser(t, bad, "renovate[bot]", "Bot", "renovate/github-actions")
			},
			wantCode:   0,
			wantExempt: true,
		},
		{
			name: "бот обновления зависимостей Go с префиксом",
			payload: func(t *testing.T) string {
				return eventWithUser(t, bad, "dependabot[bot]", "Bot", "dependabot/go_modules/x")
			},
			wantCode:   0,
			wantExempt: true,
		},
		{
			name: "человек с похожей веткой",
			payload: func(t *testing.T) string {
				return eventWithUser(t, bad, "someone", "User", "renovate/fake")
			},
			wantCode:    1,
			wantProblem: "## Цель",
		},
		{
			name: "бот с чужим префиксом",
			payload: func(t *testing.T) string {
				return eventWithUser(t, bad, "dependabot[bot]", "Bot", "renovate/fake")
			},
			wantCode:    1,
			wantProblem: "## Цель",
		},
		{
			name: "бот на обычной ветке",
			payload: func(t *testing.T) string {
				return eventWithUser(t, bad, "renovate[bot]", "Bot", "feat/other")
			},
			wantCode:    1,
			wantProblem: "## Цель",
		},
		{
			name: "логин бота с неверным регистром",
			payload: func(t *testing.T) string {
				return eventWithUser(t, bad, "Renovate[bot]", "Bot", "renovate/x")
			},
			wantCode:    1,
			wantProblem: "## Цель",
		},
		{
			name: "неполные метаданные без type",
			payload: func(t *testing.T) string {
				return eventWithUser(t, bad, "renovate[bot]", "", "renovate/x")
			},
			wantCode:    1,
			wantProblem: "## Цель",
		},
		{
			name: "пустой объект user",
			payload: func(t *testing.T) string {
				return eventJSON(t, map[string]any{
					"body": bad,
					"user": map[string]any{},
					"head": map[string]any{"ref": "renovate/x"},
				})
			},
			wantCode:    1,
			wantProblem: "## Цель",
		},
		{
			name: "отсутствующее поле user",
			payload: func(t *testing.T) string {
				return eventJSON(t, map[string]any{
					"body": bad,
					"head": map[string]any{"ref": "renovate/x"},
				})
			},
			wantCode:    1,
			wantProblem: "## Цель",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeTemp(t, "event.json", []byte(c.payload(t)))
			code, stdout, stderr := runCLI("--event-file", path)
			if code != c.wantCode {
				t.Fatalf("код выхода %d, ожидался %d: %q", code, c.wantCode, stderr)
			}
			// sender события — бот; исключение не должно срабатывать по нему.
			exempted := strings.Contains(stdout, "формат ручных PR не применяется")
			if exempted != c.wantExempt {
				t.Fatalf("признак исключения %v, ожидался %v: stdout=%q", exempted, c.wantExempt, stdout)
			}
			if c.wantExempt {
				if stderr != "" {
					t.Fatalf("неожиданная диагностика: %q", stderr)
				}
				return
			}
			if !strings.Contains(stderr, c.wantProblem) {
				t.Fatalf("нет диагностики %q: %q", c.wantProblem, stderr)
			}
			if !strings.Contains(stderr, "заглушку: ## Цель") {
				t.Fatalf("нет причины отказа по заглушке: %q", stderr)
			}
		})
	}
}

func badBody() string { return "## Цель\nTODO\n" }

func TestModesAgree(t *testing.T) {
	for name, body := range map[string]string{
		"корректное":   validBody(),
		"некорректное": badBody(),
	} {
		t.Run(name, func(t *testing.T) {
			file := writeTemp(t, "body.md", []byte(body))
			event := writeTemp(t, "event.json", []byte(eventWithUser(t, body, "someone", "User", "feat/x")))
			fileCode, fileOut, fileErr := runCLI("--body-file", file)
			eventCode, eventOut, eventErr := runCLI("--event-file", event)
			if fileCode != eventCode {
				t.Fatalf("коды различаются: файл %d, событие %d", fileCode, eventCode)
			}
			if fileOut != eventOut || fileErr != eventErr {
				t.Fatalf("вывод различается:\nфайл: %q / %q\nсобытие: %q / %q", fileOut, fileErr, eventOut, eventErr)
			}
		})
	}
}

func TestSuccessfulCLI(t *testing.T) {
	file := writeTemp(t, "body.md", []byte(validBody()))
	code, stdout, stderr := runCLI("--body-file", file)
	if code != 0 {
		t.Fatalf("код выхода %d: %q", code, stderr)
	}
	if !strings.Contains(stdout, "соответствует контракту") {
		t.Fatalf("нет подтверждения успеха: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("неожиданная диагностика: %q", stderr)
	}

	event := writeTemp(t, "event.json", []byte(eventWithUser(t, validBody(), "someone", "User", "feat/x")))
	if code, _, stderr := runCLI("--event-file", event); code != 0 {
		t.Fatalf("режим события: код %d: %q", code, stderr)
	}
}

// TestDiagnosticsDoNotEchoBody проверяет, что диагностика не печатает
// недоверенный текст целиком.
func TestDiagnosticsDoNotEchoBody(t *testing.T) {
	secret := "ПОЛНЫЙ-НЕДОВЕРЕННЫЙ-ТЕКСТ"
	body := "## Цель\n" + secret + "\n## Изменения\nTODO\n"
	file := writeTemp(t, "body.md", []byte(body))
	code, stdout, stderr := runCLI("--body-file", file)
	if code != 1 {
		t.Fatalf("код выхода %d, ожидался 1", code)
	}
	if strings.Contains(stderr, secret) || strings.Contains(stdout, secret) {
		t.Fatal("диагностика воспроизводит недоверенное описание")
	}
}
