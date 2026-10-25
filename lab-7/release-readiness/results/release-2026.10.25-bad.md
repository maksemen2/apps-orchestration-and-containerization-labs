# ShopFlow release readiness: release-2026.10.25-bad

Decision: **blocked**

Environment: staging

n8n execution: 25

Summary: Проверки показали прохождение unit и integration, но сбой в миграции из-за блокировки таблицы заказов на 14 минут. Окружение staging здоровое, с 40% свободной емкости. Для сервиса shop-api доступен план отката. Недостающая информация — отсутствует детализация по причине сбоя миграции.

Reasons:
- Required check failed: migration

Evidence and tool calls: see the adjacent JSON report.
