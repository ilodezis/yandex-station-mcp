# Настройка через AI / Setup with AI

Скорми текст ниже своему AI-агенту (Claude Code, Cursor, Antigravity, Windsurf) — он сам всё развернёт:

---

```text
Склонируй репозиторий https://github.com/ilodezis/yandex-station-mcp и настрой MCP-сервер для моей рутованной Яндекс Станции:

1. Склонируй репозиторий, если мы ещё не внутри:
   git clone https://github.com/ilodezis/yandex-station-mcp.git && cd yandex-station-mcp

2. Спроси у меня локальный IP-адрес Станции (порт Dropbear обычно 2222).

3. Сними фингерпринт хост-ключа Dropbear:
   ssh-keyscan -p <PORT> -t ed25519 <IP> 2>/dev/null | ssh-keygen -lf -
   Запиши результат (вида SHA256:...) в переменную STATION_HOST_KEY.

4. Проверь наличие SSH-ключа в ./secrets/station_key. Если его нет — сгенерируй:
   ssh-keygen -t ed25519 -N '' -C "station-mcp" -f ./secrets/station_key
   Выведи мне готовый однострочник для вставки публичного ключа на колонку:
   mount -o remount,rw / && echo "<ПУБЛИЧНЫЙ_КЛЮЧ>" >> /root/.ssh/authorized_keys && sync && mount -o remount,ro /
   Дождись, пока я подтвержу, что ключ добавлен.

5. Спроси модель колонки. Если это не Станция 2 / Миди (нет кольца подсветки), выставь LED_ENABLED=false.

6. Сгенерируй случайный токен TOKEN_AGENT (32+ символа), скопируй .env.example в .env и подставь собранные значения.

7. Запусти контейнер через docker compose up -d (или собери бинарник go build) и проверь http://127.0.0.1:8150/healthz.

8. Выдай мне готовые настройки подключения:
   - команду для Claude Code: claude mcp add ...
   - блок для claude_desktop_config.json
```
