# Лабораторна 4 — той самий граф як ADK HTTP-сервіс

**Станом на 09/2026:** Go 1.27.1, ADK Go v2.4.0. Жодного LLM, ключа або `.env` для базового шляху.
Виконуйте команди **з кореня `ai-ae-labs`**. Потрібні Go, `curl`, а для контейнера — Docker/OrbStack.

## Запуск і чотири перевірки

```bash
go run ./week2/Day4_Agent_as_Service_Deploy/labs4
```

В іншому терміналі:

```bash
BASE=http://127.0.0.1:8080
curl -fsS "$BASE/health"
curl -fsS "$BASE/readyz"
curl -fsS -X POST \
  "$BASE/api/apps/first_graph_agent/users/learner/sessions/demo" \
  -H 'Content-Type: application/json' -d '{}'
curl -fsS -N "$BASE/api/run_sse" \
  -H 'Content-Type: application/json' \
  -d '{"appName":"first_graph_agent","userId":"learner","sessionId":"demo","newMessage":{"role":"user","parts":[{"text":"Мерчант A-114 просить повернення по транзакції txn-2026-07-118845"}]}}'
```

Контрольні точки: `OK` на обох probe-маршрутах; JSON із `"id":"demo"`; потім SSE `data:` з виходами трьох вузлів, `refund:last_case_id` і статусом `pending`.
Повторіть лише SSE-запит: отримаєте `already_open`. `/api/run` також доступний через ADK як нестримінговий варіант.

`appName` — **`first_graph_agent`**, незалежно від назви контейнера чи Cloud Run service.
Це реальний `adkrest.NewServer`, не саморобний `/chat`. Поля запиту **camelCase**.
Workflow-події містять `output`; `content` може бути `null`. LLM-фрагменти тексту тут не очікуються.

## Код і перевірки

`main.go` керує сигналами й адресою; `service.go` монтує ADK під `/api/` і керує drain.
Кроки графа та інструмент імпортуються з [`week2/internal/refund`](../../internal/refund/refund.go), як у Lab 3; топологію композирує [`agent_graph.go`](agent_graph.go) цієї лаби.
Готовий базовий шлях можна змінювати у власному форку; у [завданні](Homework.md) потрібно пояснити та продемонструвати результат, а не лише скопіювати файли.

```bash
go build ./week2/...
go vet ./week2/...
go test -race ./week2/...
go test -v -run '^TestShutdown$' ./week2/Day4_Agent_as_Service_Deploy/labs4
```

Остання команда утримує активний запит і перевіряє обидва сценарії: завершення в межах drain та примусове закриття після timeout.
При SIGTERM сервіс перестає приймати нову роботу й очікує активні запити. `Shutdown` закриває listener: новий `curl` може бачити **connection refused**, а не встигнути отримати 503. На ще відкритому з'єднанні gate повертає 503. Успішний drain — exit 0, перевищення timeout — помилка й ненульовий exit.

Без `PORT` сервіс слухає `127.0.0.1:8080`; з `PORT=9090` — усі інтерфейси на 9090.
`-addr` явно перевизначає адресу. Локальна межа доступу — loopback, у контейнері — опублікований порт.

## Docker: статичний ADK-сервіс, non-root, `FROM scratch`

**Build context — корінь репозиторію**, не `labs4/`: звідти потрібні `go.mod`, `go.sum` та спільний пакет.
`Dockerfile.dockerignore` допускає лише модульні файли й потрібні Go-файли Week 2; `.env` не потрапляє в контекст.

```bash
docker build --platform linux/amd64 \
  -f week2/Day4_Agent_as_Service_Deploy/labs4/Dockerfile \
  -t ai-ae-lab4:week2 .
docker run --name ai-ae-lab4 --platform linux/amd64 --detach \
  --read-only --cap-drop ALL \
  -p 127.0.0.1:8080:8080 ai-ae-lab4:week2
```

Повторіть чотири `curl` вище. Контейнер запускає Go-бінарник як UID/GID `65532:65532`; shell і package manager у runtime немає. CA bundle включено для майбутніх HTTPS-інструментів, хоча поточний граф не робить зовнішніх викликів.
`linux/amd64` потрібна для Cloud Run; на Apple Silicon збірка cross-compile, а локальний запуск образу — через емуляцію.

```bash
docker image inspect ai-ae-lab4:week2 \
  --format 'platform={{.Os}}/{{.Architecture}} size={{.Size}} bytes user={{.Config.User}}'
docker stop --timeout 35 ai-ae-lab4
docker inspect ai-ae-lab4 --format 'exit={{.State.ExitCode}}'
docker logs ai-ae-lab4
docker rm ai-ae-lab4
```

Очікуємо `exit=0` і `ADK service drained and stopped`. Для короткого графа SIGTERM зазвичай приходить уже після відповіді; доказ активного drain дає `TestShutdown`, не цей idle-smoke.

Виміряно 24.09.2026: `linux/amd64`, Docker image inspect `.Size` = **6 735 944 bytes**, UID/GID `65532:65532`. Це результат конкретної збірки, не гарантований розмір вашого образу.

## Опційно: GCP Cloud Run

[Покроковий Go-only шлях](CLOUD_RUN.md): той самий Dockerfile → Artifact Registry → приватний щодо IAM-викликів Cloud Run → session/SSE smoke → прибирання ресурсів.
Не потрібен для локального завдання й не змінює його бали. GCP потребує billing; безкоштовність не гарантується.

## Межі й типові помилки

- **404 на SSE:** спершу створіть сесію; `appName`, `userId`, `sessionId` мають збігатися. Перезапуск втрачає сесії.
- **`event: error` після HTTP 200:** SSE вже почався; помилка вузла передається окремою подією. Самого HTTP-коду недостатньо.
- **503:** сервіс завершується. Відсутність API-ключа не впливає на readiness цього графа.
- **Порт зайнятий:** зупиніть свій попередній процес або використайте `-addr=127.0.0.1:8081`.
- **Docker не бачить `go.mod`:** перевірте build context `.` у корені `ai-ae-labs`.

Це навчальний однокористувацький сервіс без app-level авторизації, durable storage та платіжного API. Реєстр і сесії в пам'яті; не відкривайте його в загальнодоступну мережу. Cloud Run IAM обмежує викликачів сервісу, але не пов'язує довільний `userId` із людиною автоматично.
