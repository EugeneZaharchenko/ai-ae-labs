# E3 · Ambient Agent

**Хто вирішує наступний крок:** код (подія) · **Категорія:** E · людина й тригер

Агент працює як фоновий процес: запуск робить подія, а не хід людини. Це
обгортка розгортання, а не патерн оркестрації: тілом може бути будь-який патерн
A–D. Тут тіло — невеликий граф тріажу у стилі [B4](../b4_conditional_route/).

```
черга (chan ticket) → пул воркерів (2) → нова сесія на подію → ticket_body → результат
                                                                               ↓
                                             підсумок, який називає причину кожного збою

ticket_body: Start → guard → triage (LlmAgent) → file_ticket
```

## Запуск

```bash
go run .                     # розібрати симульовану чергу з 4 тікетів, без ключа
go run . -live               # triage на моделі з apps/.env
go run . console             # поговорити з тілом агента напряму
go run . web api webui       # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
📨 T-1 "Checkout page returns 500 for every customer" → ✅ filed as P1
📨 T-2 "How do I change my invoice address?" → ✅ filed as P3
📨 T-3 "" → ❌ empty ticket body
📨 T-4 "Dashboard is a bit slow in the morning" → ✅ filed as P2
summary: 4 events, 3 ok, 1 failed (T-3: empty ticket body)
```

## Go-примітиви (ADK Go v2.4.0)

| Примітив | Роль у демо |
|---|---|
| `runner.New` + `session.InMemoryService()` (через `kit.Run`) | окрема сесія на кожну подію |
| `workflowagent.New` + `workflow.Chain` | тіло воркера: guard → triage → file_ticket |
| `workflow.NewAgentNode(llmagent, cfg)` | triage як вузол графа |
| Go `chan` + `sync.WaitGroup.Go` | черга й пул воркерів — поза ADK |

Примітива E3 в ADK немає й бути не може: Pub/Sub, Eventarc і Cloud Scheduler — це
GCP, а не ADK. ADK Go дає лише транспорт (`server/adkrest`, `server/agentengine`,
`server/adka2a`). Розклад, черга й ретраї доставки — поза модулем.

## Коли брати

Людини в момент запуску немає: тікети в черзі, файл, що впав у бакет, нічний
звіт.

## Коли не брати

Інтерактивні розмовні продукти. Там людина чекає на відповідь — це A–D без
обгортки.

## Що подивитися в коді

- `guard` відхиляє зіпсовану подію **до** виклику моделі — за неї не треба платити.
- `report` — підсумок, який звітує про збій, мусить назвати причину. Ambient-збій
  тихий, доки ви не збудуєте алертинг.
- `process` створює нову сесію на кожну подію. Пам'ять між подіями потребує
  memory-сервісу, а не стану сесії.
- `TestDemoCancelled` — перервана черга звітує про необроблені події, а не мовчить.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.4.0`
