# Установка activesync-mcp на Windows (сборка через Go)

MCP-сервер для Exchange ActiveSync (EAS). Подключает Claude Desktop (или любой
MCP-клиент) к Exchange-ящику. Пароль хранится в Credential Manager (keyring),
а не в конфиге.

> В репозитории нет готовых бинарников в релизах — собирать из исходников через Go.

---

## Шаг 1. Установить Go

1. Скачайте Go для Windows: https://go.dev/dl/ (файл `go1.xx.windows-amd64.msi`).
   Нужна версия **1.26 или новее** (в `go.mod` указан `go 1.26`).
2. Запустите установщик (по умолчанию ставит в `C:\Program Files\Go`).
3. Откройте **новый** терминал (чтобы подхватились PATH и переменные) и проверьте:

```
go version
```

Должна появиться версия не ниже `go1.26`, например `go version go1.26.2 windows/amd64`.

---

## Шаг 2. Собрать и установить бинарник

> `go install github.com/avblex/activesync-mcp/...@latest` НЕ работает: в `go.mod`
> модуль объявлен как `module activesync-mcp` (без префикса репозитория),
> поэтому Go выдаёт "module declares its path as: activesync-mcp".
> Обход — клонировать репозиторий и собрать из локальной копии.

В терминале (PowerShell или cmd):

```
git clone https://github.com/avblex/activesync-mcp.git
cd activesync-mcp
go build -o activesync-mcp.exe ./cmd/activesync-mcp
```

Сборка скачает зависимости (в том числе форк библиотеки
`github.com/avblex/go-activesync`) через proxy.golang.org — нужен доступ в интернет.

После сборки в текущей папке появится `activesync-mcp.exe`.
Переместите его в папку, которая есть в PATH, например:

```
move activesync-mcp.exe %USERPROFILE%\go\bin\
```

(папку `%USERPROFILE%\go\bin` создайте, если её нет).

Добавьте `%USERPROFILE%\go\bin` в PATH, если ещё не добавлено:
- Правый клик по «Этот компьютер» → Свойства → Дополнительные параметры системы → Переменные среды.
- В системной переменной `Path` добавьте строку `%USERPROFILE%\go\bin`.
- Перезапустите терминал.

Проверка:
```
activesync-mcp help
```
Должна появиться список команд (`serve`, `setup`, `doctor`).

---

## Шаг 3. Создать конфиг (TUI-мастер)

```
activesync-mcp setup
```

Интерактивный мастер по шагам:
- найдёт EAS-endpoint через autodiscover по адресу почты (или позволит
  ввести URL вида `https://<хост>/Microsoft-Server-ActiveSync` вручную —
  уточнить можно у администратора Exchange);
- запросит пароль (без эхо) и сохранит его в Credential Manager
  под сервисом `activesync-mcp`;
- проверит соединение с сервером;
- сохранит конфиг в `%APPDATA%\activesync-mcp\config.toml`.

Файл конфига после этого можно править вручную: `server_url`, `username`
(если нужен доменный формат `USETECH\abelsky`), `allow_insecure` и т. д.

---

## Шаг 4. Проверить конфиг и подключение

```
activesync-mcp doctor
```

Ожидаемый вывод: `work: OK (12.0, 12.1, 14.0 supported; negotiated 14.0)`
или аналогичная строка с OK. Если ошибка — смотрите её текст.

---

## Шаг 5. Подключить к MCP-клиенту

В конфиге Claude Desktop
(`C:\Users\<вы>\AppData\Roaming\Claude\claude_desktop_config.json`) добавьте:

```json
{
  "mcpServers": {
    "activesync": {
      "command": "C:\\Users\\<вы>\\go\\bin\\activesync-mcp.exe",
      "args": ["serve"]
    }
  }
}
```

Поставьте полный путь к `activesync-mcp.exe` (тот, что из Шага 2).

Перезапустите Claude Desktop. В пикере инструментов появятся инструменты
аккаунта `work` (email_*, calendar_*, contacts_*, tasks_*, notes_*).

---

## Полезные команды

```
activesync-mcp serve [--config PATH]   # запустить MCP-сервер (по умолчанию)
activesync-mcp setup [--config PATH]   # мастер первичной настройки
activesync-mcp doctor [--config PATH]  # проверить конфиг + аккаунты
activesync-mcp help                    # список команд
```

Без аргументов запускается `serve`. Путь конфига по умолчанию —
`%APPDATA%\activesync-mcp\config.toml`.

---

## Если что-то не проходит

- **go: command not found** — Go не в PATH или терминал не перезапущен.
- **go build: unsupported version** — установлен Go старее 1.26, обновитесь.
- **setup: autodiscover не нашёл URL** — сервер может не отдавать autodiscover;
  введите EAS-endpoint вручную (уточните у администратора Exchange).
- **doctor: logon failed** — проверьте username (email vs DOMAIN\user)
  и что пароль в keyring именно тот, что нужен для ActiveSync.
  Переписать пароль можно заново прогнав `activesync-mcp setup`.
- **TLS-ошибка** — если сервер с самоподписанным сертификатом,
  добавьте в конфиг `allow_insecure = true` (только для доверенной среды).
- **OAuth/SSO вместо пароля** — если сервер требует современную
  авторизацию, используйте `auth = "bearer"` с `bearer_command`,
  а не basic-пароль.
