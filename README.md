# yandex-station-mcp

[🇷🇺 Русский](#-русский) | [🇬🇧 English](#-english)

[![Go Version](https://img.shields.io/badge/Go-1.27+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)
[![MCP](https://img.shields.io/badge/Protocol-MCP-orange.svg)](https://modelcontextprotocol.io/)

> **Быстрая настройка через AI:** скормите агенту (Claude Code, Cursor, Antigravity) файл [**`SETUP_WITH_AI.md`**](SETUP_WITH_AI.md) со ссылкой на репозиторий — он сам снимет фингерпринт, настроит ключи, запустит Docker и выдаст конфиг.
> 
> **Quick AI Setup:** feed [**`SETUP_WITH_AI.md`**](SETUP_WITH_AI.md) to your agent (Claude Code, Cursor, etc.) — it will fetch the repo, configure keys, run Docker, and return the connection snippet.

---

## 🇷🇺 Русский

Локальный [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) сервер, превращающий рутованную **Яндекс Станцию** в физический интерфейс для AI-агентов. Даёт агентам реальный голос в комнате, цветовую индикацию статусов на светодиодном кольце, аппаратные таймеры, управление медиаплеером и датчик освещённости.

Работает как единый статический бинарник на Go или легковесный Docker-контейнер.

### Почему не облако

Существующие решения для Яндекс Станций обычно построены на костылях: реверс приватного мобильного API, авторизация через QR-коды, создание виртуальных сценариев в Яндекс Умном Доме и постоянно протухающие токены. Если Яндекс обновляет веб-интерфейс или отваливается интернет — интеграция умирает.

`yandex-station-mcp` устроен принципиально иначе:
* **Прямой доступ к ОС колонки:** Сервер общается напрямую с Станцией по локальной сети без облаков и протухающих токенов.
* **Штатные утилиты железа:** В SSH-режиме речь воспроизводится через системный бинарник `say`, звук и громкость управляются через ALSA (`amixer`, `aplay`), подсветка рисуется напрямую во фреймбуфер `/dev/fb0`.
* **Нативные директивы через сокет:** Встроенная утилита `station-ctl` шлёт бинарные Protobuf-директивы (`TYPE_DIRECTIVE`, `TYPE_REMOTING`) напрямую в локальный UNIX-сокет демона квазара `/tmp/yio_ipc`. Плеер и ночной режим переключаются моментально, без голосовых команд в микрофон.
* **Автономность:** Если сервер перезагружается или падает сеть, уже запущенные на колонке таймеры гарантированно сработают.

### Поддерживаемые бэкенды (SSH vs Luna)

Сервер поддерживает два бэкенда взаимодействия с колонкой (`STATION_BACKEND` в `.env`):

1. **SSH бэкенд (`STATION_BACKEND=ssh`, по умолчанию):**
   * Общается напрямую с Linux колонки по локальной сети через Dropbear SSH (порт 2222, ключ Ed25519, пиннинг фингерпринта хост-ключа).
   * Полный спектр возможностей: нативный голосовой синтез через `say`, ALSA (`amixer`, `aplay`), подсветка через фреймбуфер `/dev/fb0`, чтение люксометра `/sys/bus/...`, бинарные Protobuf-директивы плеера и DND через сокет `/tmp/yio_ipc` и автономные таймеры прямо на колонке.
2. **Luna HTTP бэкенд (`STATION_BACKEND=luna`):**
   * Общается с HTTP API открытого демона [nyakokitsu/luna](https://github.com/nyakokitsu/luna) (по умолчанию порт 8099, `LUNA_ADDR=http://<IP_СТАНЦИИ>:8099`).
   * Не требует генерации SSH-ключей, записи в `/root/.ssh/authorized_keys` и проверки хост-ключей.
   * Управляет громкостью (`/api/volume`), медиаплеером (`/api/media/command`), кольцом подсветки (`/api/led/ring`), чтением сенсоров освещённости (`/api/sensors`), аудиосигналами (`/api/audio/test`) и таймерами в памяти.
   * *Примечание:* Синтез речи (TTS) и режим DND в HTTP API Luna не предусмотрены и возвращают ошибку при вызове.

### Аппаратная адаптация

Проект разрабатывался и отлаживался на **Яндекс Станции 2**, но архитектурно разделён:
1. **Универсальная база:** Речь, ALSA-звуки, громкость, сокет директив плеера, таймеры и политика уведомлений работают на любых рутованных колонках с Алисой на базе Linux.
2. **Аппаратные датчики и подсветка:** Верхнее RGBW-кольцо (`/dev/fb0`) и люксометр LTR-308 требуют соответствующего железа (Станция 2, Миди). Если на вашей модели нет кольца, подсветка отключается одной переменной `LED_ENABLED=false` (тул `led` скрывается, а ночные уведомления уходят в тишину).

### Человеко-ориентированная политика уведомлений

Агент не должен орать голосом посреди ночи из-за упавшего фонового билда. Инструмент `announce` содержит встроенную матрицу доставки:
* **Днём:** Все сообщения озвучиваются голосом. Действует квота `VOICE_BUDGET` (по умолчанию не более 10 реплик за 10 минут), чтобы зациклившийся агент не устраивал спам-атаку.
* **Ночью (`quiet mode`):** Обычные уведомления (`info`) сбрасываются в тишину. Важные (`important`) зажигают янтарное кольцо на 5 минут. Критические (`critical`) зажигают красное кольцо на 20 минут.
* **Watchdog подсветки:** Когда Алиса слушает пользователя или говорит сама, она перерисовывает кольцо. Фоновый цикл сервера на колонке отслеживает состояние: пока играет анимация Алисы — сервер не мешает; как только кольцо гаснет — статусная подсветка агента восстанавливается.

---

### Доступные инструменты (Tools)

| Инструмент | Параметры | Описание | Профиль `agent` | Профиль `brain` |
|---|---|---|---|---|
| `announce` | `text`, `urgency` (`info`, `important`, `critical`) | Умное уведомление с учётом времени суток и квот. | Да | Нет |
| `set_volume` | `percent` (0–100) | Установка мастер-громкости (ограничена потолком `MAX_VOLUME`). | Да | Да |
| `status` | *(нет)* | Доступность, громкость, аптайм, активные таймеры, состояние кольца, люксы, ночной режим. | Да | Да |
| `ambient_light` | *(нет)* | Чтение уровня света в люксах и флага темноты с датчика LTR-308. | Да | Да |
| `player_control` | `action` (`play`, `pause`, `next`, `prev`, `toggle`) | Управление воспроизведением медиаплеера. | Да | Да |
| `dnd` | `enabled` (`true`, `false`) | Включение/выключение режима «Не беспокоить» (DND, SSH). | Да | Да |
| `play_sound` | `name` (`timer`, `success`, `notice`, `error`) | Воспроизведение коротких системных звуковых сигналов (WAV/PCM). | Да | Да |
| `timer` | `seconds`, `text` | Таймер: звуковой сигнал по истечении. Срабатывает всегда. | Да | Да |
| `led` | `color`, `brightness`, `fraction`, `hold_seconds` | Управление кольцом: цвета (имена/hex), шкала прогресса (`fraction`), удержание. | Да | Да |

---

### Быстрый старт

#### 1. Клонирование репозитория

```bash
git clone https://github.com/ilodezis/yandex-station-mcp.git
cd yandex-station-mcp
```

#### 2. Конфигурация (.env)

```bash
cp .env.example .env
```

**Вариант А: SSH бэкенд (по умолчанию)**
1. Создайте пару SSH-ключей для общения со Станцией:
   ```bash
   mkdir -p secrets
   ssh-keygen -t ed25519 -N '' -C "station-mcp" -f ./secrets/station_key
   ```
2. Добавьте `./secrets/station_key.pub` в `/root/.ssh/authorized_keys` на Станции:
   ```bash
   mount -o remount,rw / && echo "<СОДЕРЖИМОЕ_station_key.pub>" >> /root/.ssh/authorized_keys && sync && mount -o remount,ro /
   ```
3. Снимите фингерпринт ключа колонки:
   ```bash
   ssh-keyscan -p 2222 -t ed25519 <IP_СТАНЦИИ> 2>/dev/null | ssh-keygen -lf -
   ```
4. В `.env` задайте:
   * `STATION_BACKEND=ssh`
   * `STATION_ADDR`: IP адрес и порт колонки (например, `192.168.1.100:2222`).
   * `STATION_HOST_KEY`: полученный SHA256 фингерпринт Dropbear.

**Вариант Б: Luna HTTP бэкенд**
1. В `.env` задайте:
   * `STATION_BACKEND=luna`
   * `LUNA_ADDR`: адрес Luna API (например, `http://192.168.1.100:8099`). Если не указан, порт 8099 берется от `STATION_ADDR`.
2. Генерация SSH-ключей не требуется.

Общие параметры в `.env`:
* `TOKEN_AGENT`: сгенерируйте случайную строку (`openssl rand -base64 36`).
* `LED_ENABLED`: выставьте `false`, если на колонке нет кольца подсветки.

#### 3. Запуск

Через Docker Compose:
```bash
docker compose up -d
```

Или чистым бинарником на Go:
```bash
go build -o yandex-station-mcp .
./yandex-station-mcp
```

Проверка здоровья:
```bash
curl http://127.0.0.1:8150/healthz
# Вернет: ok
```

---

### Подключение клиентов

#### Claude Code / Claude Desktop

Подключение через CLI:
```bash
claude mcp add --scope user --transport http station https://your-domain.com/mcp
```

Или в конфиге `claude_desktop_config.json`:
```json
{
  "mcpServers": {
    "station": {
      "url": "https://your-domain.com/mcp",
      "headers": {
        "Authorization": "Bearer ВАШ_TOKEN_AGENT"
      }
    }
  }
}
```

#### Claude.ai (Custom Connector через OAuth 2.0 PKCE)

В веб-версии claude.ai нельзя вручную передать кастомный Bearer-заголовок — сервис требует полноценный OAuth 2.0 PKCE поток. `yandex-station-mcp` содержит встроенный легковесный OAuth-сервер:

1. В `.env` задайте `PUBLIC_URL` (ваш внешний HTTPS), `OAUTH_PASSWORD` (пароль для входа) и сгенерируйте секреты `OAUTH_TOKEN_SECRET` и `OAUTH_CLIENT_SECRET`.
2. В веб-интерфейсе **claude.ai -> Settings -> Connectors -> Add Custom Connector**:
   * URL: `https://your-domain.com/mcp`
   * Client ID: `station-claude-connector`
   * Client Secret: ваш `OAUTH_CLIENT_SECRET`
3. Нажмите **Connect**. Откроется веб-страница авторизации со стилизованной панелью Станции. Введите `OAUTH_PASSWORD` — коннектор готов к работе.

---

## 🇬🇧 English

A local [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that turns a rooted **Yandex Station** (Linux-based smart speaker with Alice) into a physical interface for AI agents. Grants agents real-world voice output, RGBW LED status feedback, persistent hardware timers, native playback controls, and ambient room light sensing.

Runs as a single static Go binary or lightweight Docker container.

### Why Not Cloud

Most third-party smart speaker integrations rely on fragile workarounds: reverse-engineered private mobile APIs, QR-code authentication, creating synthetic scenarios in cloud dashboards, and managing tokens that silently expire.

`yandex-station-mcp` is designed differently:
* **Direct Speaker Access:** Communicates locally across your LAN with zero dependency on vendor clouds.
* **Native OS Utilities:** Under the SSH backend, speech runs via the built-in `say` binary, audio and volume are controlled through ALSA (`amixer`, `aplay`), and lighting writes directly to the `/dev/fb0` framebuffer.
* **Direct IPC Socket Directives:** Built-in helper `station-ctl` sends binary Protobuf frames (`TYPE_DIRECTIVE`, `TYPE_REMOTING`) straight into the local Quazar UNIX socket `/tmp/yio_ipc`. Music and Do Not Disturb are toggled instantly with zero acoustic delay.
* **Independent Timers:** Timers execute as isolated background processes directly on the speaker (`timer`), surviving network drops and server restarts.

### Supported Backends (SSH vs Luna)

The server supports two backend connection modes (`STATION_BACKEND` in `.env`):

1. **SSH Backend (`STATION_BACKEND=ssh`, default):**
   * Connects locally via Dropbear SSH (port 2222, Ed25519 key authentication, pinned host key fingerprint).
   * Full feature set: speech synthesis via `say`, ALSA (`amixer`, `aplay`), `/dev/fb0` framebuffer LED rendering, LTR-308 sysfs sensor readings, low-latency Protobuf directives via `station-ctl`, and standalone on-speaker timers.
2. **Luna HTTP Backend (`STATION_BACKEND=luna`):**
   * Connects to the HTTP API of the open-source [nyakokitsu/luna](https://github.com/nyakokitsu/luna) runtime daemon (default port 8099, `LUNA_ADDR=http://<STATION_IP>:8099`).
   * Zero SSH keys, host key fingerprinting, or `/root/.ssh/authorized_keys` editing needed.
   * Supports volume control (`/api/volume`), media playback directives (`/api/media/command`), LED ring lighting (`/api/led/ring`), ambient light sensors (`/api/sensors`), audio signals (`/api/audio/test`), and in-memory timers.
   * *Note:* TTS speech synthesis and DND directives are not supported by the Luna HTTP API and return a clear error when invoked.

### Hardware Adaptation

Developed and tuned on **Yandex Station 2**, with modular boundaries:
1. **Core Features:** Voice synthesis, ALSA sounds, volume, IPC playback directives, timers, and notification policies work across rooted Alice Linux speakers in general.
2. **Lighting & Sensors:** The top RGBW LED ring (`/dev/fb0`) and the LTR-308 lux sensor require hardware support. For speakers without an LED ring, disable it via `LED_ENABLED=false` (the `led` tool is hidden, and night alerts fall back to silence).

### Human-Centric Announce Policy

Your AI agent shouldn't scream in the middle of the night because a background unit test failed. The `announce` tool implements contextual routing:
* **Daytime:** Announcements play via speech synthesis, bounded by a `VOICE_BUDGET` quota (default 10 alerts per 10 minutes).
* **Nighttime (`quiet mode`):** Routine notifications (`info`) are dropped silently. Important alerts (`important`) pulse an amber ring for 5 minutes. Critical failures (`critical`) trigger a glowing red ring for 20 minutes.
* **Watchdog Persistence:** Whenever Alice is actively listening or talking, she overwrites `/dev/fb0`. A lightweight on-speaker loop detects when the ring goes dark and restores the agent's status frame without interfering with Alice's active animations.

---

### Tools Overview

| Tool | Parameters | Description | `agent` Profile | `brain` Profile |
|---|---|---|---|---|
| `announce` | `text`, `urgency` (`info`, `important`, `critical`) | Context-aware notifications (voice by day, ambient glow by night). | Yes | No |
| `set_volume` | `percent` (0–100) | Master volume adjustment (capped at `MAX_VOLUME`). | Yes | Yes |
| `status` | *(none)* | Speaker health, volume, uptime, active timers, LED state, lux, quiet mode. | Yes | Yes |
| `ambient_light` | *(none)* | Ambient illuminance reading (lux and dark boolean) from LTR-308. | Yes | Yes |
| `player_control` | `action` (`play`, `pause`, `next`, `prev`, `toggle`) | Direct media playback control. | Yes | Yes |
| `dnd` | `enabled` (`true`, `false`) | Toggles native Do Not Disturb (DND) mode (SSH). | Yes | Yes |
| `play_sound` | `name` (`timer`, `success`, `notice`, `error`) | Low-latency system audio beeps (WAV/PCM). | Yes | Yes |
| `timer` | `seconds`, `text` | Timer: alert sound when done. | Yes | Yes |
| `led` | `color`, `brightness`, `fraction`, `hold_seconds` | RGBW top ring control with progress bar (`fraction`) and hold loops. | Yes | Yes |

---

### Quick Start

#### 1. Clone the Repository

```bash
git clone https://github.com/ilodezis/yandex-station-mcp.git
cd yandex-station-mcp
```

#### 2. Configuration (.env)

```bash
cp .env.example .env
```

**Option A: SSH Backend (Default)**
1. Generate an SSH keypair for the speaker:
   ```bash
   mkdir -p secrets
   ssh-keygen -t ed25519 -N '' -C "station-mcp" -f ./secrets/station_key
   ```
2. Add `./secrets/station_key.pub` to `/root/.ssh/authorized_keys` on the speaker:
   ```bash
   mount -o remount,rw / && echo "<CONTENTS_OF_station_key.pub>" >> /root/.ssh/authorized_keys && sync && mount -o remount,ro /
   ```
3. Retrieve the speaker host key fingerprint:
   ```bash
   ssh-keyscan -p 2222 -t ed25519 <STATION_IP> 2>/dev/null | ssh-keygen -lf -
   ```
4. Set in `.env`:
   * `STATION_BACKEND=ssh`
   * `STATION_ADDR`: Speaker IP and port (e.g. `192.168.1.100:2222`).
   * `STATION_HOST_KEY`: Fingerprint string (`SHA256:...`).

**Option B: Luna HTTP Backend**
1. Set in `.env`:
   * `STATION_BACKEND=luna`
   * `LUNA_ADDR`: Luna API address (e.g. `http://192.168.1.100:8099`). If omitted, port 8099 is used with `STATION_ADDR`.
2. No SSH keys or host key verification required.

General parameters in `.env`:
* `TOKEN_AGENT`: Random token (`openssl rand -base64 36`).
* `LED_ENABLED`: Set `false` if your model lacks an LED ring.

#### 3. Run

With Docker Compose:
```bash
docker compose up -d
```

Or Go binary:
```bash
go build -o yandex-station-mcp .
./yandex-station-mcp
```

Health check:
```bash
curl http://127.0.0.1:8150/healthz
# Output: ok
```

---

## License

[GNU General Public License v3.0 (GPL-3.0)](LICENSE) © 2026 Timur Burak
