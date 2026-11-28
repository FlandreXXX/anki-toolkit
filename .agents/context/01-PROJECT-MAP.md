# Карта репозитория

## Назначение

`anki-toolkit` — нативное Go-приложение для Linux и Windows x86-64.
CLI предоставляет справку, версию и диагностику собственной сборки,
конфигурации и путей. Внутреннее ядро читает и сериализует экспорты CrowdAnki.
Прикладные операции с колодами относятся к будущим этапам.
В репозитории также действуют пять переносимых агентских скиллов и два
самостоятельных Go-валидатора метаданных PR.

## Структура

```text
AGENTS.md                          # постоянные ограничения и маршрутизация
README.md                          # сборка, команды и конфигурация
LICENSE                            # GNU AGPL v3
.gitignore                         # локальные артефакты задач и бинарники в корне
go.mod                             # корневой модуль и точная версия Go
cmd/anki-toolkit/                  # точка входа и тест собранного бинарника
internal/
├── app/                           # прикладные операции, исходы и статусы
├── cli/                           # аргументы и представление результатов
├── config/                        # строгий JSON и платформенные пути
├── crowdanki/                     # документ, разбор, сериализация и связи CrowdAnki
├── crowdankifs/                   # чтение deck.json на Linux и Windows
└── version/                       # каноническая версия программы
docs/
├── architecture.md                # границы, машинный протокол и политика версии
├── crowdanki.md                   # поддерживаемый формат и API
└── threat-model.md                # поверхность и границы доверия
.agents/
├── context/
│   ├── INDEX.md                   # индекс владельцев и маршрутизация
│   ├── 01-PROJECT-MAP.md          # этот файл
│   └── 03-VERIFICATION.md         # фактические проверки и их границы
├── local/                         # игнорируется Git: артефакты задач по git-workflow
└── skills/
    ├── git-workflow/
    │   ├── SKILL.md
    │   └── references/
    │       ├── publication.md
    │       ├── pr-body.md
    │       └── pr-lifecycle.md
    ├── go-best-practices/
    │   ├── SKILL.md
    │   └── references/
    │       ├── idiomatic-go.md
    │       ├── software-design.md
    │       ├── reuse-and-dependencies.md
    │       └── reliability-and-performance.md
    ├── testing/
    │   ├── SKILL.md
    │   └── references/
    │       ├── test-design.md
    │       ├── go-testing.md
    │       └── regression-and-coverage.md
    ├── systematic-debugging/
    │   ├── SKILL.md
    │   └── references/
    │       ├── diagnostic-workflow.md
    │       ├── scope-and-escalation.md
    │       └── go-diagnostics.md
    └── coderabbit-review/
        ├── SKILL.md
        └── references/
            ├── review-workflow.md
            └── review-report.md
.github/
├── scripts/
│   ├── pr-body-lint/
│   │   ├── main.go
│   │   └── main_test.go
│   └── coderabbit-comment-lint/
│       ├── main.go
│       └── main_test.go
└── workflows/
    ├── pr-body.yml                # задача CI `Структура PR`
    └── product.yml                # продуктовые проверки на Linux и Windows
```

Корневой Go-модуль содержит приложение и не требует сторонних модулей.
Обе утилиты под `.github/` — самостоятельные `package main`, которые запускаются и тестируются
**именованными файлами** из корня репозитория (точные команды —
[`03-VERIFICATION.md`](03-VERIFICATION.md)). Каталог `.agents/local/` целиком
игнорируется Git и в структуру публикуемых файлов не входит.

## Точки входа

- [`AGENTS.md`](../../AGENTS.md) — обязательные постоянные правила для любого
  агента; читается первым.
- [`.agents/context/INDEX.md`](INDEX.md) — маршрутизация к владельцам вопросов.
- `.agents/skills/<name>/SKILL.md` — точка входа скилла: контракт, активация,
  границы; детали — в его `references/`.
- [`cmd/anki-toolkit/main.go`](../../cmd/anki-toolkit/main.go) — исполняемая точка
  входа и сборка зависимостей.
- `.github/workflows/pr-body.yml` и `.github/workflows/product.yml` — отдельные
  проверки структуры PR и продукта.
- `.github/scripts/*/main.go` — исполняемые проверки; `.github/scripts/*/main_test.go`
  — их собственные тесты.

## Кто чем владеет

- Документ [`docs/architecture.md`](../../docs/architecture.md) описывает владельцев
  продуктовых компонентов и контрактов. Границы доверия описаны в
  [`docs/threat-model.md`](../../docs/threat-model.md).
- `AGENTS.md` — постоянные ограничения: язык, объём и полномочия, доказательность
  проверок, Git и публикация. Процедуры здесь не дублируются.
- `systematic-debugging` ([`SKILL.md`](../skills/systematic-debugging/SKILL.md)) —
  доказательная локализация причины и эскалация препятствий. Процесс,
  границы и Go-инструменты разделены в
  [`diagnostic-workflow.md`](../skills/systematic-debugging/references/diagnostic-workflow.md),
  [`scope-and-escalation.md`](../skills/systematic-debugging/references/scope-and-escalation.md) и
  [`go-diagnostics.md`](../skills/systematic-debugging/references/go-diagnostics.md).
- `git-workflow` ([`SKILL.md`](../skills/git-workflow/SKILL.md)) — общая процедура
  публикации: ветка, индекс, коммит, push, проверка точного удалённого SHA,
  создание и повторное чтение PR, Draft/Ready, конфликты с base, отдельно
  разрешённый merge. Детали — [`publication.md`](../skills/git-workflow/references/publication.md),
  [`pr-body.md`](../skills/git-workflow/references/pr-body.md),
  [`pr-lifecycle.md`](../skills/git-workflow/references/pr-lifecycle.md).
- `coderabbit-review` ([`SKILL.md`](../skills/coderabbit-review/SKILL.md)) — цикл
  явно запрошенного CodeRabbit: активация, число итераций, вызов провайдера,
  доказательство завершённого ревью, оценка замечаний. Процедура —
  [`review-workflow.md`](../skills/coderabbit-review/references/review-workflow.md).
- `go-best-practices` ([`SKILL.md`](../skills/go-best-practices/SKILL.md)) — Go-специфика
  качества кода, включая тестовый: идиомы языка, дизайн, зависимости,
  надёжность и производительность. Общая инженерная методология и независимое
  ревью остаются за Ponytail.
- `testing` ([`SKILL.md`](../skills/testing/SKILL.md)) — выбор проверяемых
  гарантий, сценариев и уровней, оценка доказательств. Детали разделены между
  [`test-design.md`](../skills/testing/references/test-design.md),
  [`go-testing.md`](../skills/testing/references/go-testing.md) и
  [`regression-and-coverage.md`](../skills/testing/references/regression-and-coverage.md).
  Для Go применяется вместе с `go-best-practices`; фактические команды
  проверок принадлежат [`03-VERIFICATION.md`](03-VERIFICATION.md).
- [`review-report.md`](../skills/coderabbit-review/references/review-report.md) —
  единственный владелец формата, истории и специализированной публикации
  накопительного комментария CodeRabbit.
- `pr-body-lint` ([`main.go`](../../.github/scripts/pr-body-lint/main.go)) —
  структура описания PR по снимку файла или события; без сети.
- `coderabbit-comment-lint`
  ([`main.go`](../../.github/scripts/coderabbit-comment-lint/main.go)) — чистая
  проверка Markdown накопительного отчёта и сверка опубликованного состояния
  только на чтение. Структуру описания PR он не проверяет: это отдельная утилита.
- Ponytail — общая инженерная методология и независимое ревью, а
  `go-best-practices` — её специализация для Go. Ни один из них не расширяет
  полномочия и не отменяет согласованный контракт задачи.

Второй владелец одной и той же процедуры не заводится: Git-процесс принадлежит
`git-workflow`, цикл CodeRabbit — `coderabbit-review`, формат отчёта —
`review-report.md`, проверки — своим утилитам и CI.

## Как находить место изменения

1. Определи область: постоянные ограничения, систематическая диагностика и
   эскалация, тестирование, публикация, цикл CodeRabbit, формат отчёта, утилита
   проверки или CI.
2. Открой владельца из таблицы в [`INDEX.md`](INDEX.md), а не пиши процедуру
   заново.
3. Для Go-тестов применяй `testing` вместе с `go-best-practices`. Для
   утилит помни: Go-модуля нет, запуск и тесты — именованными файлами.
4. После изменения выполни применимые проверки из
   [`03-VERIFICATION.md`](03-VERIFICATION.md).
