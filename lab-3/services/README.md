# API и worker

API принимает `POST /order` с телом `{"item":"book","quantity":2}` и возвращает последние 100 заказов через `GET /orders`. Worker отмечает ожидающие заказы как `processed`. Выбор и обновление выполняются одним SQL-запросом с `FOR UPDATE SKIP LOCKED`, поэтому два worker могут работать параллельно.

Оба сервиса предоставляют:

- `/health` — 200, при `HEALTH_FAIL=true` — 503;
- `/ready` — готовность таблицы и соединения с PostgreSQL;
- `/metrics` — запросы, ошибки, время ответа, готовность БД и обработанные заказы.

Для запуска нужен `DATABASE_URL`; порт задаётся через `PORT` (по умолчанию 8080). Таблица `orders` создаётся автоматически. В Kubernetes строка подключения берётся из Secret `shop-db-app`, который создаёт CloudNativePG.

Общий Dockerfile принимает `SERVICE=api` или `SERVICE=worker` и `APP_VERSION`. Версия видна в заголовке `X-App-Version`. Контейнер работает от пользователя 10001.
