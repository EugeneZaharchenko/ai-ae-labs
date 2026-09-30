# agentgateway: локальний LLM-шлюз + спостережуваність

> **Трасування перевірено наживо** (agentgateway v1.5.0, 2026-09-23): запит до
> `:4000` дає дві спани в Jaeger. Решта стека на цій машині не піднімалася
> повністю — перед заняттям пройдіть `docker compose up -d` і звірте, що всі
> сервіси стартують, перш ніж обіцяти це студентам.

Один OpenAI-сумісний вхід на `:4000`, за яким стоять шість бекендів — від
фейкового мока до OpenAI, Anthropic, Gemini й Ollama Cloud. Кожен виклик
отримує трасу (Jaeger), метрики (Prometheus → Grafana) і **реалізовану
вартість у USD** у рядку access-log.

> **Станом на 08/2026.** Перевірено проти **agentgateway v1.4.1**. Назви полів
> конфігу звіряйте перед записом — схема змінюється між мінорними релізами.
> Джерело правди для полів, ужитих тут: `schema/config.json` у теґу `v1.4.1`.

Навчальний матеріал, який пояснює *навіщо* це на першому тижні:
[`courses/AI_Agents_Engineering/lectures/week1/Day1_Models_and_Frameworks_Landscape/Local_Monitoring_Agentgateway.md`](https://github.com/dimetron/prometheus-courses/blob/main/courses/AI_Agents_Engineering/lectures/week1/Day1_Models_and_Frameworks_Landscape/Local_Monitoring_Agentgateway.md).

---

## Запуск

```bash
cd demo/1_ai-gateway
docker compose up -d
```

**Ключі не потрібні.** Стек підіймається порожнім, і бекенд `mock-gpt`
відповідає без жодного API-ключа й без жодної копійки витрат:

```bash
curl -s http://localhost:4000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock-gpt","messages":[{"role":"user","content":"привіт"}]}'
```

| Сервіс | Порт | Навіщо |
|---|---|---|
| `agentgateway` | `:4000` | OpenAI-сумісний API — сюди дивиться ваш агент |
| `agentgateway` (admin) | `:15000` | адмін-UI |
| `mock-llm` | `:8080` | фейковий бекенд, працює без ключів |
| `jaeger` | `:16686` | траси |
| `prometheus` | `:9090` | метрики |
| `grafana` | `:3000` | дашборди (анонімний вхід, роль Admin) |

---

## Траси: Jaeger

**http://localhost:16686/** — вкладка **Search**, сервіс `agentgateway`.

Після першого запиту до `:4000` у списку з'являється сервіс `agentgateway`, а в
ньому — дві спани на кожен виклик: вхідний `POST /*` і вихідний
`POST <upstream>` (напр. `generativelanguage.googleapis.com:443`). Обидві несуть
тривалість, тож видно, скільки часу з'їв сам шлюз, а скільки — провайдер.

```bash
curl -s http://localhost:4000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer <AGENTGATEWAY_API_KEY>' \
  -d '{"model":"mock-gpt","messages":[{"role":"user","content":"привіт"}]}'
# далі відкрийте http://localhost:16686/ і натисніть Find Traces
```

Трасування вмикається блоком `tracing` у `config/agentgateway.yaml`. Дві речі в
ньому неочевидні:

- **`randomSampling: true` обов'язковий.** Типово шлюз не семплює нічого: без
  цього поля запити проходять успішно, а Jaeger лишається порожнім.
- **Endpoint — `jaeger:4317`, а не `localhost`.** Шлюз працює в контейнері, тож
  `localhost:4317` указував би на нього самого. `4317` — gRPC-приймач Jaeger;
  `4318` (HTTP) з цим endpoint не працює.

Своєму колектору трасування можна віддати через `AGENTGATEWAY_OTLP_ENDPOINT`.
Ім'я навмисно не `OTEL_EXPORTER_OTLP_ENDPOINT`: останнє є стандартним, його
ставлять інші інструменти, і підхоплене з оточення значення на кшталт
`127.0.0.1:4317` тихо зламало б трасування.

> Траси зберігаються **в пам'яті** `jaeger`-контейнера: після
> `docker compose restart jaeger` список порожній. Це навмисно — демо не має
> накопичувати базу трас на хості.

---

## Бекенди й ключі

Скопіюйте `.env.example` у `.env` і заповніть **тільки ті рядки, для яких у вас
є ключ**. Порожній `.env` — валідний: усе, що ви не заповнили, просто не
відповідатиме, а `mock-gpt` працюватиме як і раніше.

| Модель у запиті | Провайдер | Змінна | Реально виконується |
|---|---|---|---|
| `mock-gpt` | локальний мок | — | контейнер `mock-llm` |
| `gpt-4o-mini` | OpenAI | `OPENAI_API_KEY` | api.openai.com |
| `claude-sonnet` | Anthropic | `ANTHROPIC_API_KEY` | api.anthropic.com |
| `gemini-flash` | Google Gemini | `GEMINI_API_KEY` | generativelanguage.googleapis.com |
| `deepseek-cloud` | **Ollama Cloud** | `OLLAMA_API_KEY` | **ollama.com — це хмара, не ваш ноутбук** |
| `ollama-local` | локальна Ollama | — | ваш `localhost:11434` (закоментовано) |

### Як вибрати бекенд

Іменем моделі в запиті — більше ніде нічого міняти не треба:

```bash
curl -s http://localhost:4000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-flash","messages":[{"role":"user","content":"привіт"}]}'
```

Той самий рядок працює з `AGENTGATEWAY_BASE_URL=http://localhost:4000/v1` з
агента: шлюз — це просто інша адреса, жодного SDK і жодної залежності в
`go.mod`. Список доступних імен віддає `GET http://localhost:4000/v1/models`.

Окремо є **віртуальні моделі** — імена, за якими шлюз сам обирає бекенд:
`smart` (90% на мок, 10% на OpenAI), `resilient` (failover з мертвого бекенда
на живий), `tiered` (вибір за заголовком `x-tier`).

### Ollama Cloud ≠ локальна Ollama

Найпоширеніша хибна здогадка цього тижня — «це ж Ollama, значить локально».
У конфізі навмисно є **обидва** бекенди на одному й тому самому провайдері
`ollama`, і різниця між ними рівно одна — `params.baseUrl`:

```yaml
# Backend 6 — хмара: токени залишають вашу машину
baseUrl: https://ollama.com/v1
apiKey: "$OLLAMA_API_KEY"

# Backend 7 — ноутбук: дефолт пресета ollama, ключ не потрібен
baseUrl: http://host.docker.internal:11434/v1
```

**Пастка з іменем моделі.** Через локальний демон ці моделі мають тег
`:cloud` — саме він каже вашому ноутбуку віддати роботу в хмару
(`ollama run deepseek-v4-flash:cloud`; 284B параметрів на ноутбук і не влізли
б). Коли ви ходите на `https://ollama.com/v1` **напряму**, тега `:cloud` немає
й додавати його нікуди — потрібні id зі списку
`GET https://ollama.com/v1/models` (перевірено наживо 2026-08-20:
`deepseek-v4-flash:preview`, `gpt-oss:120b`, `kimi-k3`, `glm-5.2`,
`qwen3.5:397b`, …). Звіряйте список перед записом.

Якщо вам потрібне саме «дані не виходять із периметра» — це `ollama-local`
з малою моделлю, а не Ollama Cloud.

Бекенд `ollama-local` залишено закоментованим свідомо: `host.docker.internal`
резолвиться на Docker Desktop / OrbStack, але не на голому Linux Docker без
`extra_hosts`. Розкоментуйте, якщо у вас запущена локальна Ollama.

---

## Дві деталі, на яких легко спіткнутися

**1. Кожна `$ЗМІННА` в конфізі має існувати в оточенні контейнера.**
agentgateway shell-розгортає весь конфіг під час завантаження, і
**невизначена** змінна — це фатальна помилка старту, тоді як **порожня** —
цілком нормально. Тому `docker-compose.yml` передає кожен ключ як
`${VAR:-}`: «не заданий на хості» перетворюється на «порожній у контейнері».
Саме це і тримає офлайн-шлях робочим. Додаєте новий `$VAR` у
`config/agentgateway.yaml` — додайте його і в `environment:` компоуза.

**2. Ключ каталогу цін ≠ імʼя провайдера в конфізі.**
`config/catalog.json` шукає ціни за точним збігом рядка, без аліасів, і
Gemini репортує себе як `gcp.gemini`, а не `gemini`. Vertex AI був би
`gcp.vertex_ai`, Bedrock — `aws.bedrock`. Помилитеся ключем — `cost_total`
у access-log мовчки стане нулем.

Ціни в каталозі — станом на 08/2026, за 1M токенів:

| Провайдер / модель | Вхід | Вихід | Примітка |
|---|---|---|---|
| `openai` / `gpt-4o-mini` | $0.15 | $0.60 | |
| `anthropic` / `claude-sonnet-4-0` | $3.00 | $15.00 | |
| `gcp.gemini` / `gemini-3.7-flash` | $0.75 | $3.75 | **вступна ціна до 31.12.2026**, далі $1.50 / $7.50 |
| `ollama` / `deepseek-v4-flash:preview` | 0 | 0 | див. нижче |
| `mock` / `mock-gpt` | $0.15 | $0.60 | вигадані, щоб було що показати |

Нулі для Ollama Cloud — не помилка й не «безкоштовно»: Ollama Cloud тарифікує
**підпискою й лімітами використання**, а не за токен, тож перевести його в
$/1M чесно неможливо. Колонка `cost_total` для нього показуватиме 0 — і це
сама по собі корисна ілюстрація до лекції: калькулятор вартості показує лише
те, що ви в нього поклали.

---

## Що показує кожна секція конфігу

| Секція | Демонструє |
|---|---|
| `config.tracing` | OTLP-експорт трас у локальний колектор |
| `config.modelCatalog` | каталог цін per-provider / per-model |
| `frontendPolicies.accessLog` | `cost_total` / `cost_input` / `cost_output` у кожному рядку логу |
| `gateways` / `llm` | OpenAI-сумісний API на `:4000` |
| `llm.models` | шість бекендів, шість провайдерів |
| `llm.virtualModels` | weighted / failover / conditional маршрутизація |
| `llm.models[].guardrails` | reject запиту з credentials, маскування email у відповіді |
| `llm.models[].promptCaching` | prompt caching на боці провайдера |

---

## Джерела

- [agentgateway.dev/docs/standalone/latest](https://agentgateway.dev/docs/standalone/latest) — документація standalone-режиму
- Схема конфігу: <https://agentgateway.dev/schema/config>
- [docs.ollama.com/cloud](https://docs.ollama.com/cloud) — Ollama Cloud, автентифікація й ліміти

Матеріали, з яких виріс цей стек:

- https://dev.to/anup_sharma_86fa94612fe3c/i-built-an-ai-that-decides-which-ai-to-talk-to-running-247-from-my-living-room-211p
- https://dev.to/anup_sharma_86fa94612fe3c/i-traced-personal-agents-source-code-inside-was-pi-and-it-dreams-at-3-am-o0f
- https://dev.to/anup_sharma_86fa94612fe3c/giving-agentgateway-a-semantic-brain-with-vllm-semantic-router-inside-my-homelab-542f
- https://dev.to/anup_sharma_86fa94612fe3c/adding-observability-to-my-ai-homelab-3dbj
