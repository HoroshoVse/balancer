# Balancer

Balancer — это современный, высокопроизводительный балансировщик нагрузки (Load Balancer), написанный на Go (Backend) и React (Frontend).

## Основные возможности

- **TCP/UDP & HTTP балансировка:** Поддержка всех основных сетевых протоколов, включая HTTP/3 (QUIC).
- **Продвинутая маршрутизация:** `Round Robin`, `Least Connections`, `Weighted Round Robin` и `IP Hash`.
- **Auto SSL (Let's Encrypt):** Автоматический выпуск и продление SSL-сертификатов (ACME).
- **Prometheus Metrics:** Встроенный сбор метрик по адресу `/metrics` (RPS, Задержка, Ошибки).
- **Telegram Уведомления:** Оповещения в Telegram в реальном времени при падении или восстановлении серверов (настраивается в UI).
- **Поддержка PROXY Protocol (v1/v2):** Прозрачная передача реальных IP-адресов клиентов на бекенды.
- **Инжект HTTP Заголовков:** Автоматическая установка `X-Real-IP` и `X-Forwarded-For`.
- **Аутентификация & CLI:** Встроенный инструмент командной строки для безопасного управления пользователями и JWT защита API.

---



### Шаг 1. Скачивание конфигурации
```bash
git clone https://github.com/HoroshoVse/balancer.git
cd balancer
```


```

### Шаг 2. Запуск балансировщика

```bash
docker compose pull
docker compose up -d
```

### Шаг 3. Доступ к интерфейсу
Откройте в браузере: `http://<IP_вашего_сервера>:3000`
По умолчанию для входа используйте:
- Логин: `admin`
- Пароль: `admin`

---

## 🛠 Управление пользователями (CLI)

В состав бэкенда входит встроенная утилита командной строки `balancer-cli`. Для работы с ней выполняйте команды внутри запущенного Docker-контейнера.

**Просмотр списка всех пользователей:**
```bash
docker compose exec backend ./balancer-cli users list
```

**Добавление нового пользователя:**
```bash
docker compose exec backend ./balancer-cli users add <username> <password> [role]

```

**Смена пароля:**
```bash
docker compose exec backend ./balancer-cli users passwd <username> <new_password>
```

---

## Технический стек
- **Backend:** Go (1.22), PostgreSQL, Prometheus client, Certmagic (AutoSSL).
- **Frontend:** React (20), TypeScript, Tailwind CSS, Shadcn/ui, Recharts.
- **CI/CD:** GitHub Actions (авто-сборка и пуш в GHCR).
