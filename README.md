# Balancer

Balancer — это современный, высокопроизводительный production-ready балансировщик нагрузки (Load Balancer), написанный на Go (Backend) и React (Frontend).

## Основные возможности

- **TCP/UDP & HTTP балансировка:** Поддержка всех основных сетевых протоколов.
- **Алгоритмы распределения:** `Round Robin` и `Least Connections`.
- **Поддержка PROXY Protocol (v1/v2):** Прозрачная передача реальных IP-адресов клиентов на бекенды.
- **Инжект HTTP Заголовков:** Автоматическая установка `X-Real-IP` и `X-Forwarded-For`.
- **Современный UI:** Красивая админ-панель для управления серверами и просмотра статистики (построена на Tailwind CSS + Shadcn/ui).
- **Раздельный мониторинг:** Сбор метрик (RPS, Задержка, % ошибок) по каждому балансировщику в реальном времени.
- **CLI Утилита:** Встроенный инструмент командной строки для безопасного управления пользователями.
- **Аутентификация:** Защита API и UI с помощью JWT.

---

## Быстрый старт (Установка через Docker)

Для развертывания проекта вам понадобятся установленные **Docker** и **Docker Compose**.

1. **Клонируйте репозиторий:**
   ```bash
   git clone https://github.com/HoroshoVse/balancer.git
   cd balancer
   ```

2. **Запустите проект:**
   ```bash
   docker compose up -d
   ```
   Docker Compose автоматически поднимет базу данных (PostgreSQL), соберет Backend (включая CLI утилиту) и запустит Frontend (на Vite dev-server для среды разработки).

3. **Доступ к интерфейсу:**
   Откройте в браузере: [http://localhost:5173](http://localhost:5173)
   По умолчанию для входа используйте:
   - Логин: `admin`
   - Пароль: `admin`

---

## Управление пользователями (CLI)

В состав бэкенда входит встроенная утилита командной строки `balancer-cli`. Для работы с ней выполняйте команды внутри Docker-контейнера.

**Просмотр списка всех пользователей:**
```bash
docker compose exec backend ./balancer-cli users list
```

**Добавление нового пользователя:**
```bash
docker compose exec backend ./balancer-cli users add <username> <password> [role]
# Пример: docker compose exec backend ./balancer-cli users add developer qwerty1234 Admin
```

**Смена пароля:**
```bash
docker compose exec backend ./balancer-cli users passwd <username> <new_password>
# Пример: docker compose exec backend ./balancer-cli users passwd admin super_secure_pass
```

---

## Разработка

Проект разделен на две основные папки:

- **`/backend`** — содержит исходный код на Go.
  - `cmd/server/main.go` — Точка входа основного сервера балансировщика и API.
  - `cmd/cli/main.go` — Точка входа утилиты командной строки.
- **`/frontend`** — содержит исходный код на TypeScript / React (Vite).
  - Сборка UI осуществляется командой `npm run build`.

## Технический стек
- **Backend:** Go, PostgreSQL (pgx/gorm), github.com/pires/go-proxyproto, golang-jwt.
- **Frontend:** React, TypeScript, Tailwind CSS, Shadcn/ui, Vite.
