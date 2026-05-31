# kinopub

CLI-утилита для скачивания видео с [kino.pub](https://kino.pub) в полном качестве — все аудиодорожки (с названиями студий озвучки), все субтитры, мультисезонные сериалы.

## Возможности

- Скачивание по ссылке на страницу (`/item/view/...`) или по прямой ссылке на podcast feed
- Все аудиодорожки с метаданными (студия озвучки)
- Все субтитры
- Выбор сезонов и эпизодов (`--seasons 1,3-5 --episodes 1-12`)
- Выбор качества (`-q 1080p`)
- Прогресс-бар в терминале
- Возобновление прерванных загрузок (state-файл)
- Поддержка прокси (HTTP, HTTPS, SOCKS5)
- Контейнеры MKV (по умолчанию) и MP4
- Зашифрованное хранение авторизации (`kinopub login`)

## Установка

### Из релизов (рекомендуется)

Скачайте бинарник для вашей платформы со [страницы релизов](../../releases/latest):

| Платформа | Архитектура | Файл |
|-----------|-------------|------|
| macOS | Apple Silicon (M1/M2/M3) | `kinopub-darwin-arm64` |
| macOS | Intel | `kinopub-darwin-amd64` |
| Linux | x86_64 | `kinopub-linux-amd64` |
| Linux | ARM64 (Termux, Raspberry Pi) | `kinopub-linux-arm64` |

```bash
# Пример для macOS Apple Silicon:
chmod +x kinopub-darwin-arm64
mv kinopub-darwin-arm64 /usr/local/bin/kinopub

# Пример для Linux:
chmod +x kinopub-linux-amd64
sudo mv kinopub-linux-amd64 /usr/local/bin/kinopub
```

### Из исходников

```bash
go install kinopub_downloader/cmd/kinopub@latest
```

Или:

```bash
git clone https://github.com/YOUR_USERNAME/kinopub_downloader.git
cd kinopub_downloader
go build -o kinopub ./cmd/kinopub
```

## Зависимости

- **ffmpeg** — для мультиплексирования потоков (видео + аудио + субтитры). Должен быть в `$PATH` или указан через `--ffmpeg`.
  ```bash
  # macOS
  brew install ffmpeg

  # Ubuntu/Debian
  sudo apt install ffmpeg

  # Termux
  pkg install ffmpeg
  ```

## Быстрый старт

### 1. Авторизация

kino.pub защищён Cloudflare. Для скачивания нужны куки из залогиненного браузера.

**Способ 1: Копирование куки из DevTools (работает везде)**

1. Откройте kino.pub в браузере, залогиньтесь
2. Откройте DevTools (F12) → Network
3. Обновите страницу, кликните на первый запрос
4. Скопируйте значение заголовка `Cookie` из Request Headers
5. Скопируйте значение `User-Agent`

```bash
kinopub login \
  --cookie "cf_clearance=...; _identity=...; PHPSESSID=..." \
  --user-agent "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) ..."
```

**Способ 2: Автоматическая загрузка из браузера (только macOS)**

Требует Full Disk Access для терминала (System Settings → Privacy & Security → Full Disk Access).

```bash
kinopub login --browser-cookies safari
```

> Credentials сохраняются зашифрованными в `~/.config/kinopub/credentials.enc` и привязаны к конкретному устройству (расшифровать на другой машине невозможно).

### 2. Скачивание

```bash
# По ссылке на страницу (самый простой способ)
kinopub -o ./downloads https://kino.pub/item/view/38290

# По прямой ссылке на podcast feed (не требует авторизации)
kinopub -o ./downloads https://kino.pub/podcast/get/38290/TOKEN

# Только 1 сезон, 1080p
kinopub -o ./downloads --seasons 1 -q 1080p https://kino.pub/item/view/38290

# Посмотреть что будет скачано (без загрузки)
kinopub --dry-run https://kino.pub/item/view/38290
```

### 3. Удаление авторизации

```bash
kinopub logout
```

## Автоматическая загрузка куки из браузера

| Платформа | Safari | Chrome | Firefox |
|-----------|--------|--------|---------|
| macOS | ✅ (Full Disk Access) | ⚠️ (Keychain) | ✅ |
| Linux | — | ✅ | ✅ |
| Termux | — | — | — |

- **macOS Safari**: Требует Full Disk Access для терминала
- **macOS Chrome**: Куки зашифрованы через Keychain, может потребоваться разрешение
- **Linux Chrome/Firefox**: Работает если профиль не зашифрован
- **Termux**: Автозагрузка из браузера недоступна, используйте ручное копирование куки

## Все флаги

```
kinopub [flags] <url>
kinopub login [flags]       — сохранить авторизацию
kinopub logout              — удалить сохранённую авторизацию

Основные:
  -o, --output          Директория для сохранения (по умолчанию: текущая)
  -q, --quality         Предпочтительное качество (например: 1080p, 720p, 480p)
  --seasons             Выбор сезонов (например: 1,3-5)
  --episodes            Выбор эпизодов (например: 1-12)
  --container           Формат контейнера: mkv (по умолчанию) или mp4
  --dry-run             Показать список эпизодов без скачивания
  --force               Перекачать уже скачанные эпизоды

Сеть:
  --proxy               URL прокси (http://, https://, socks5://)
  -c, --concurrency     Макс. параллельных загрузок (по умолчанию: 2, макс: 16)
  --retries             Макс. попыток при ошибке (по умолчанию: 5)
  --min-interval        Мин. интервал между запросами в мс

Авторизация:
  --cookie              Cookie header (для разового использования)
  --user-agent          User-Agent (должен совпадать с браузером куки)
  --browser-cookies     Загрузить куки из браузера: safari, chrome, firefox, auto
  --header              Доп. HTTP заголовок 'Name: Value' (можно повторять)

Прочее:
  --ffmpeg              Путь к ffmpeg (по умолчанию: ffmpeg в PATH)
  --ffmpeg-args         Доп. аргументы ffmpeg строкой (advanced, см. ниже)
  --x                   Доп. аргумент ffmpeg (повторяемый, advanced, см. ниже)
  --feed-file           Использовать локальный RSS/XML файл вместо сети
  --log-file            Путь к файлу лога
  -v                    Подробный вывод (debug)
  --verbosity           Уровень логирования: quiet, normal, verbose
  --version             Показать версию
```

## Продвинутое: аргументы ffmpeg

Можно передать дополнительные аргументы напрямую в ffmpeg. Они вставляются перед выходным файлом, что позволяет переопределить `-c copy` (stream copy) на перекодирование или добавить фильтры.

**Два способа:**

```bash
# Способ 1: строка (парсится с учётом кавычек)
kinopub --ffmpeg-args "-c:v libx265 -crf 28 -c:a aac" https://kino.pub/item/view/38290

# Способ 2: повторяемый --x (точный контроль каждого аргумента)
kinopub --x "-c:v" --x libx265 --x "-crf" --x 28 https://kino.pub/item/view/38290

# Можно комбинировать
kinopub --ffmpeg-args "-c:v libx265" --x "-crf" --x 28 https://kino.pub/item/view/38290
```

**Примеры использования:**

```bash
# Перекодировать видео в H.265 (HEVC) для экономии места
kinopub --ffmpeg-args "-c:v libx265 -crf 28 -c:a copy" <url>

# Перекодировать аудио в AAC (для совместимости с MP4)
kinopub --container mp4 --ffmpeg-args "-c:v copy -c:a aac -b:a 192k" <url>

# Уменьшить разрешение до 720p
kinopub --ffmpeg-args "-c:v libx264 -vf scale=-1:720 -crf 23 -c:a copy" <url>

# Добавить хардсаб (вшить субтитры в видео)
kinopub --ffmpeg-args "-c:v libx264 -vf subtitles=input.srt -c:a copy" <url>
```

> **Важно:** при указании `-c:v` или `-c:a` вы переопределяете дефолтный `-c copy`. ffmpeg применяет последний указанный кодек, поэтому ваши аргументы имеют приоритет.

## Хранение авторизации

Credentials шифруются AES-256-GCM с ключом, производным от уникального идентификатора машины:

| Платформа | Источник ключа |
|-----------|---------------|
| macOS | IOPlatformUUID (аппаратный UUID Mac) |
| Linux | `/etc/machine-id` (systemd) |
| Termux | `$PREFIX/etc/machine-id` или `/proc/sys/kernel/random/boot_id` |

Это означает:
- Файл `~/.config/kinopub/credentials.enc` бесполезен на другой машине
- Даже при краже файла, без знания machine-id расшифровка невозможна
- На Termux: если используется `boot_id`, credentials сбросятся после перезагрузки. Рекомендуется создать `/data/data/com.termux/files/usr/etc/machine-id` вручную:
  ```bash
  uuidgen > $PREFIX/etc/machine-id
  ```

## Структура выходных файлов

```
downloads/
└── Название сериала/
    ├── Season 01/
    │   ├── S01E01 - Название эпизода.mkv
    │   ├── S01E02 - Название эпизода.mkv
    │   └── ...
    ├── Season 02/
    │   └── ...
    └── .kinopub-state.json    ← прогресс загрузки
```

## Сборка релизов

```bash
# macOS Apple Silicon
GOOS=darwin GOARCH=arm64 go build -o kinopub-darwin-arm64 ./cmd/kinopub

# macOS Intel
GOOS=darwin GOARCH=amd64 go build -o kinopub-darwin-amd64 ./cmd/kinopub

# Linux x86_64
GOOS=linux GOARCH=amd64 go build -o kinopub-linux-amd64 ./cmd/kinopub

# Linux ARM64 (Termux, Raspberry Pi)
GOOS=linux GOARCH=arm64 go build -o kinopub-linux-arm64 ./cmd/kinopub
```

## Лицензия

MIT
