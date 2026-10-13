# Лабораторна 4 — той самий граф як ADK HTTP-сервіс

**Станом на 09/2026:** Go 1.27.1, ADK Go v2.5.0. Жодного LLM, ключа або `.env` для базового шляху.
Виконуйте команди **з кореня `ai-ae-labs`**. Потрібні Go, `curl`, а для контейнера — Docker/OrbStack.

## ДЗ 4 — що зроблено

Станом на 10/2026: Go 1.27.1, `google.golang.org/adk/v2 v2.5.0` (пін з `go.mod`, не `@latest`),
Docker builder `golang:1.27.1`, Docker 29.6.2 (Docker Desktop, Windows 11, engine `linux/amd64`).
Усе нижче — мої власні запуски 13–14.10.2026, а не скопійовані числа.

### Що я змінив

- `agent_graph.go` — це мій агент з ДЗ 3 (`newStaticGraph` з `labs3`): `Start → prepare → open_refund_case → format`.
  Роутер `classify` з labs3 я сюди не брав: у сервісу одна задача — відкрити кейс, тому кожен запит іде через tool-вузол.
  Ім'я агента — `first_graph_agent` (`refund.AppName`), і саме його треба передавати як `appName`.
- `graph_test.go` — тест, що ім'я агента = `first_graph_agent`, бо інакше REST поверне 404 `failed to find`.
- `service.go`, `main.go`, `Dockerfile` — база курсу, я їх перевірив і пояснюю нижче, що вони роблять.

### Як зібрати й запустити

```bash
go build ./week2/...
go test -race ./week2/...
go run ./week2/Day4_Agent_as_Service_Deploy/labs4          # локально, 127.0.0.1:8080

# статичний бінарник окремо (те саме робить Dockerfile)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
  -o adk-service ./week2/Day4_Agent_as_Service_Deploy/labs4

# контейнер FROM scratch, тег явний — week2, не latest
docker build --platform linux/amd64 -f week2/Day4_Agent_as_Service_Deploy/labs4/Dockerfile -t ai-ae-lab4:week2 .
docker run --name ai-ae-lab4 --platform linux/amd64 --detach --read-only --cap-drop ALL \
  -p 127.0.0.1:8080:8080 ai-ae-lab4:week2
```

`task week2:day4:docker:*` на Windows не працює (Taskfile лише для macOS/Linux), тому я запускав команди вручну.

### 4 curl-перевірки (локальний запуск)

```bash
BASE=http://127.0.0.1:8080
```

**1. Liveness** — процес живий:

```
$ curl -i $BASE/health
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
Content-Length: 3

OK
```

**2. Readiness** — сервіс приймає роботу (не в drain):

```
$ curl -i $BASE/readyz
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
Content-Length: 3

OK
```

**3. Створення сесії** (без неї `run_sse` дає 404):

```
$ curl -X POST $BASE/api/apps/first_graph_agent/users/learner/sessions/demo \
    -H 'Content-Type: application/json' -d '{}'
{"id":"demo","appName":"first_graph_agent","userId":"learner","lastUpdateTime":1791925405,"events":[],"state":{}}
```

**4. SSE-запит** через `curl -N` — видно три окремі `data:` події, по одній на вузол:

```
$ curl -N -i $BASE/api/run_sse -H 'Content-Type: application/json' \
    -d '{"appName":"first_graph_agent","userId":"learner","sessionId":"demo","newMessage":{"role":"user","parts":[{"text":"Мерчант A-114 просить повернення по транзакції txn-2026-07-118845"}]}}'
HTTP/1.1 200 OK
Cache-Control: no-cache
Connection: keep-alive
Content-Type: text/event-stream; charset=UTF-8
Transfer-Encoding: chunked

data: {"id":"2a16bc0e-89c7-41ce-a960-1c06dec1f043","invocationId":"e-eeb47d29-6ca7-49d1-b749-c4ef482cf8ef","author":"first_graph_agent","groundingMetadata":null,"usageMetadata":null,"actions":{"stateDelta":{},"artifactDelta":{}},"output":{"transaction_id":"txn-2026-07-118845","merchant_id":"A-114"},"nodeInfo":{"path":"first_graph_agent@1/prepare@1","outputFor":["first_graph_agent@1/prepare@1"]},"content":null}

data: {"id":"626517d8-7fae-4d33-bb8b-c03093d787d9","invocationId":"e-eeb47d29-6ca7-49d1-b749-c4ef482cf8ef","author":"first_graph_agent","groundingMetadata":null,"usageMetadata":null,"actions":{"stateDelta":{"refund:last_case_id":"rc-txn-2026-07-118845-A-114","refund:last_merchant_id":"A-114","refund:last_status":"pending"},"artifactDelta":{}},"output":{"case_id":"rc-txn-2026-07-118845-A-114","merchant_id":"A-114","status":"pending","transaction_id":"txn-2026-07-118845"},"nodeInfo":{"path":"first_graph_agent@1/open_refund_case@1","outputFor":["first_graph_agent@1/open_refund_case@1"]},"content":null}

data: {"id":"70356881-16e9-4e2c-8faa-7dfe2ec481fb","invocationId":"e-eeb47d29-6ca7-49d1-b749-c4ef482cf8ef","author":"first_graph_agent","groundingMetadata":null,"usageMetadata":null,"actions":{"stateDelta":{},"artifactDelta":{}},"output":"Кейс rc-txn-2026-07-118845-A-114: транзакція txn-2026-07-118845, мерчант A-114, статус pending","nodeInfo":{"path":"first_graph_agent@1/format@1","outputFor":["first_graph_agent@1/format@1","first_graph_agent@1"]},"content":null}
```

Кейс відкриває саме вузол `open_refund_case` — `stateDelta` з `refund:last_case_id` є лише в його події.
Той самий запит вдруге в тій самій сесії дає `"status":"already_open"` (повторного кейсу немає).
`content` = `null`, бо тут немає LLM — вузли графа віддають `output`.

### Graceful shutdown

Як це працює (`main.go` + `service.go`):

1. `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` — SIGTERM/Ctrl+C скасовує контекст.
2. `serve` бачить `ctx.Done()` і ставить `draining = true`: `/readyz` і `/api/...` тепер віддають **503**, а `/health` — далі 200
   (процес живий, просто не бере нову роботу — тому liveness і readiness різні).
3. `server.Shutdown(ctx з -drain-timeout, типово 30s)` закриває listener і чекає активні запити.
   Нове з'єднання після цього може отримати **connection refused**, а не 503 — listener уже закритий.
4. Якщо drain не вклався в timeout — `server.Close()` рве з'єднання, `serve` повертає помилку → `log.Fatal` → exit ≠ 0.
   Якщо вклався — `ADK service drained and stopped` і exit 0.

SIGTERM у контейнері (`docker stop` шле саме SIGTERM, у Dockerfile `STOPSIGNAL SIGTERM`):

```
$ docker stop --timeout 35 ai-ae-lab4
ai-ae-lab4                                   # зупинився за 0.66 s
$ docker inspect ai-ae-lab4 --format 'exit={{.State.ExitCode}}'
exit=0
$ docker logs ai-ae-lab4
2026/10/13 21:06:35 ADK service listening at [::]:8080 (no model credentials required)
2026/10/13 21:06:38 draining: rejecting new work and waiting for active requests
2026/10/13 21:06:38 ADK service drained and stopped
```

Тут активних запитів не було (граф встиг відповісти до сигналу), тому це idle-stop. Активний drain і timeout
перевіряє тест з утримуваним HTTP-запитом:

```
$ go test -v -run '^TestShutdown$' ./week2/Day4_Agent_as_Service_Deploy/labs4
=== RUN   TestShutdown
=== RUN   TestShutdown/drains_active_request
2026/10/14 00:03:14 draining: rejecting new work and waiting for active requests
=== RUN   TestShutdown/timeout_closes_active_request
2026/10/14 00:03:14 draining: rejecting new work and waiting for active requests
--- PASS: TestShutdown (0.07s)
    --- PASS: TestShutdown/drains_active_request (0.07s)
    --- PASS: TestShutdown/timeout_closes_active_request (0.00s)
PASS
ok  	github.com/dimetron/ai-eng-course/labs/week2/Day4_Agent_as_Service_Deploy/labs4	0.121s
```

`drains_active_request` — запит тримається, сервіс не виходить, поки він не завершиться.
`timeout_closes_active_request` — drain = 0, `serve` повертає `context.DeadlineExceeded`, запит закривається.

### Контейнер `FROM scratch`: розмір і що в ньому є

Виміряно 14.10.2026, `linux/amd64`, Docker 29.6.2:

```
$ docker image inspect ai-ae-lab4:week2 \
    --format 'platform={{.Os}}/{{.Architecture}} size={{.Size}} bytes user={{.Config.User}}'
platform=linux/amd64 size=6751428 bytes user=65532:65532

$ docker history ai-ae-lab4:week2 --format '{{.Size}}\t{{.CreatedBy}}'
0B      ENTRYPOINT ["/adk-service"]
0B      STOPSIGNAL SIGTERM
0B      EXPOSE [8080/tcp]
0B      ENV PORT=8080
0B      USER 65532:65532
242kB   COPY /etc/ssl/certs/ca-certificates.crt /etc…
16.9MB  COPY /out/adk-service /adk-service # buildkit
```

- `.Size` = **6 751 428 bytes (≈6.75 MB)** — це стиснутий розмір образу. Розпакований — ≈17.1 MB (бінарник 16.9 MB + CA 242 kB).
  `docker image ls` на Docker Desktop показує `DISK USAGE 23.9MB` — туди ще входить attestation-маніфест, тому я беру `.Size`.
- Склад runtime повністю перелічуваний: **один статичний бінарник `/adk-service` + `ca-certificates.crt`**. Немає shell, libc, package manager.
- CA bundle додано явно, хоча мій граф зараз не робить HTTPS-викликів — він знадобиться, якщо агент піде до LLM-провайдера.
- Бінарник статичний (`CGO_ENABLED=0`), інакше в `scratch` він би не стартував — а тут стартує.
- Запуск non-root (`65532:65532`), `--read-only`, `--cap-drop ALL`. Тег — `week2`, не `latest` (вимога Security).

Ті самі 4 curl у контейнері дали: `OK`, `OK`, JSON з `"id":"demo"`, і SSE з вузлами `prepare` → `open_refund_case` (`"status":"pending"`) → `format`.

### Межі

Це навчальний сервіс: немає авторизації, сесії й реєстр кейсів в пам'яті (рестарт = все зникає, повторне створення сесії `demo` дає 500 `already exists`).
Локально слухає лише `127.0.0.1`, контейнер публікую теж лише на `127.0.0.1`.

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

Найкоротший шлях — шість задач із кореневого `Taskfile.yml`. Вони роблять те
саме, що команди нижче, але з правильними теками, тегами й перевірками:

```bash
task week2:day4:docker:build     # docker build --platform linux/amd64 -f ... -t ai-ae-lab4:week2 .
task week2:day4:docker:run       # docker run --detach --read-only --cap-drop ALL -p 127.0.0.1:8080:8080
task week2:day4:docker:smoke     # чотири curl-перевірки одною задачею
task week2:day4:docker:stop      # SIGTERM + доказ drain: exit=0, «drained and stopped»
task week2:day4:docker:inspect   # platform, size, user
task week2:day4:docker:rm        # прибрати контейнер (образ лишається)
```

| Змінна | Типове | Навіщо |
|---|---|---|
| `PORT` | `8080` | публікований порт на loopback; `PORT=18080`, якщо 8080 зайнятий |
| `DOCKER_PLATFORM` | `linux/amd64` | одна платформа для `docker:build` і `docker:run`; `DOCKER_PLATFORM=linux/arm64` — швидша нативна збірка на Apple Silicon (не для Cloud Run) |
| `DOCKER_PLATFORMS` | `linux/amd64,linux/arm64` | список для `docker:multiarch` |
| `REGISTRY` | — | обов'язковий для `docker:multiarch`: префікс реєстру для `--push` |

`task week2:day4:docker:run` спершу прибирає одноіменний контейнер, тож задачу
можна запускати повторно: без цього `docker run` падає з «name is already in
use» після першого перерваного запуску.

`task week2:day4:docker:smoke` створює сесію `demo`. Сесії in-memory і живуть
у процесі сервісу, тож **повторний виклик smoke на тому самому контейнері**
віддає `500` з `session demo already exists` — це стан сервісу, не помилка
образу. Щоб повторити з чистого стану:

```bash
docker rm -f ai-ae-lab4 && task week2:day4:docker:run
```

### Образ на дві архітектури (`docker:multiarch`)

Додаткова задача для публікації: збирає `linux/amd64` і `linux/arm64` одним
manifest-індексом і одразу штовхає його в реєстр. `Dockerfile` уже готовий до
цього крос-компіляцією (`FROM --platform=$BUILDPLATFORM` + `TARGETOS/TARGETARCH`),
тож друга архітектура не коштує нічого зайвого.

```bash
task week2:day4:docker:multiarch REGISTRY=ghcr.io/<ваш-акаунт>
```

```bash
# Той самий виклик зі змінною оточення — форма, зручна для CI
REGISTRY=europe-west1-docker.pkg.dev/<project>/adk-lab4-images \
  task week2:day4:docker:multiarch
```

Тег виходить `<REGISTRY>/ai-ae-lab4:week2-linux-amd64-linux-arm64`, тож
multiarch-образ не перезаписує локальний `ai-ae-lab4:week2`, з яким працюють
`docker:run` і `docker:smoke`.

Три речі, які варто знати перед запуском:

- **`--push` обов'язковий, а не опція.** buildx не вміє `--load` від індексу з
  кількома платформами, і локальний smoke усе одно не запуститься під чужу
  архітектуру. Тому `docker:multiarch` — про публікацію; локально
  перевіряйте `docker:build` + `docker:run`.
- **`--provenance=false`** тримає маніфест пласким. Cloud Run читає простий
  індекс: без attestation у ньому менше шарів і `gcloud run deploy` не має
  зайвих сутностей.
- **Для Cloud Run ця задача не потрібна.** Він вимагає лише `linux/amd64`, тож
  швидший шлях — `docker buildx build --platform linux/amd64 --push` з
  [CLOUD_RUN.md](CLOUD_RUN.md). Multiarch має сенс, коли той самий образ
  беруть і amd64-сервер, і Apple Silicon/ARM-хост — тоді обидва тягнуть свою
  архітектуру з одного тега, без емуляції.

Явні команди, якщо `task` не встановлено:

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

Перевірено ще раз 01.10.2026 тими самими задачами (`task week2:day4:docker:*`,
Docker 29.4.0 / OrbStack, macOS arm64): `linux/amd64` — **6 751 693 bytes**
(через емуляцію повільніше), `linux/arm64` — **6 137 830 bytes**.
Обидві збірки дали `user=65532:65532`. Чотири перевірки пройшли в контейнері:
`OK`, `OK`, JSON з `"id":"demo"`, три SSE-події з `"status":"pending"`; повторний
SSE на тому ж контейнері — `"status":"already_open"`. `task week2:day4:docker:stop`
дав `exit=0` і `ADK service drained and stopped`.

`task week2:day4:docker:multiarch` перевірено того ж дня з локальним `registry:2`
як `REGISTRY`: **36.9 s** на обидві архітектури (BuildKit-кеш уже теплий від
збірок вище; на холодному кеші буде довше). Результат — індекс
`application/vnd.oci.image.index.v1+json` рівно з двома маніфестами,
`linux/amd64` і `linux/arm64`, без attestation-записів (`--provenance=false`).
Локальна збірка знову дала `platform=linux/amd64 size=6751693 bytes user=65532:65532`.

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
