# Опційно — ADK Go сервіс у Google Cloud Run

**Станом на 09/2026**, адаптовано з [ADK: Deploy to Cloud Run](https://adk.dev/deploy/cloud-run/), зокрема вкладки **Go**. Це додаткова практика, не вимога локального ДЗ і не новий критерій оцінювання.

Результат: образ з [Dockerfile](Dockerfile) працює в Cloud Run, доступ дозволений лише IAM-викликачам, запити використовують той самий Go REST-контракт.
Нижче наведені команди для **вашого навчального проєкту**. Їх виконання створює платні ресурси. Спершу перевірте billing, бюджетні сповіщення та права; бюджетне сповіщення не є жорстким лімітом витрат.

## Чому не `adk deploy cloud_run`

Офіційна сторінка має окремі Python, TypeScript, Go та Java інструкції.
Для Go є **`adkgo deploy cloudrun`**, який компілює entry point, генерує образ і запускає **launcher** з аргументами `web`, `api`, `a2a`.
Lab 3 має launcher, але Lab 4 навмисно керує `http.Server` і `adkrest` напряму; передати її `main.go` цьому launcher-deployer не можна як взаємозаміну.

Тому тут використовуємо **Docker + `gcloud run deploy --image`**: зберігаємо `/health`, `/readyz`, власний drain та перевірений package build. Не потрібні Python, FastAPI, `requirements.txt`, A2A або dev UI.
Приклади `app_name`/`new_message` з Python-вкладки також не копіюємо: у нашому Go API — `appName`/`newMessage` та префікс `/api/`.

## Передумови

Потрібні `gcloud`, Docker з buildx, доступ до навчального GCP-проєкту з увімкненим billing і пройдений локальний [Docker smoke](README.md).
Виконуйте команди з кореня `ai-ae-labs`.

Адміністратор має дозволити вам увімкнення API, створення Artifact Registry repo/service account та деплой Cloud Run. Для повторних деплоїв потрібні доступ на запис до конкретного image repo, Cloud Run deploy-права та `roles/iam.serviceAccountUser` на runtime account. Зміна invoker binding потребує прав зміни IAM сервісу. Не видавайте runtime account ролі Owner/Editor чи build-admin.

```bash
export PROJECT_ID="your-training-project"
export REGION="europe-west1"
export SERVICE="adk-lab4"
export REPOSITORY="adk-lab4-images"
export RUNTIME_SA="adk-lab4-runtime"
export INVOKER_EMAIL="your-google-account@example.com"
export IMAGE="$REGION-docker.pkg.dev/$PROJECT_ID/$REPOSITORY/adk-service:week2-v1"

gcloud auth login
gcloud services enable run.googleapis.com artifactregistry.googleapis.com iam.googleapis.com \
  --project="$PROJECT_ID"
gcloud artifacts repositories create "$REPOSITORY" \
  --repository-format=docker --location="$REGION" --project="$PROJECT_ID"
gcloud iam service-accounts create "$RUNTIME_SA" --project="$PROJECT_ID" \
  --display-name="ADK Lab 4 runtime"
gcloud auth configure-docker "$REGION-docker.pkg.dev"
```

Створення repo/account виконується один раз. Якщо ім'я зайняте, перевірте власника й призначення ресурсу, не видаляйте його і не приховуйте помилку.
Runtime account цього no-model графа не потребує доступу до Gemini, Vertex AI чи Secret Manager. Не додавайте ключ і не створюйте JSON-ключ service account.
Cloud Build у цьому шляху не використовується: образ збираєте локально.

## Образ і деплой

Cloud Run потребує `linux/amd64`, навіть якщо ноутбук — Apple Silicon:

// CLI - `brew install --cask gcloud-cli`


```bash
docker buildx build --platform linux/amd64 \
  -f week2/Day4_Agent_as_Service_Deploy/labs4/Dockerfile \
  -t "$IMAGE" --push .

gcloud run deploy "$SERVICE" \
  --project="$PROJECT_ID" --region="$REGION" \
  --image="$IMAGE" \
  --service-account="$RUNTIME_SA@$PROJECT_ID.iam.gserviceaccount.com" \
  --no-allow-unauthenticated \
  --execution-environment=gen2 \
  --port=8080 --args=-drain-timeout=8s \
  --cpu=1 --memory=512Mi \
  --min-instances=0 --max-instances=1 --concurrency=1 --timeout=120

gcloud run services add-iam-policy-binding "$SERVICE" \
  --project="$PROJECT_ID" --region="$REGION" \
  --member="user:$INVOKER_EMAIL" --role=roles/run.invoker
```

`--no-allow-unauthenticated` залишає IAM-перевірку: публічна URL-адреса не означає публічний доступ. Не додавайте `allUsers` і не вимикайте invoker IAM check.

Cloud Run передає `PORT`; наш бінарник слухає всі інтерфейси на цьому порту. TLS завершується на Cloud Run, усередині контейнера — HTTP. `scratch` не має shell, тому підстановка `$PORT` зроблена **Go-кодом**, не shell-командою в `CMD`.

Після SIGTERM платформа дає **10 секунд** перед SIGKILL, тому тут drain = **8 секунд**, а не локальні 30. Довільно довгий запит не гарантовано завершиться: timeout повідомляється як помилка. ADK SSE write deadline і Cloud Run request timeout у цій вправі — 120 секунд; це не механізм durable execution.

## Перевірка через автентифікований proxy

У першому терміналі, під акаунтом із `roles/run.invoker`:

```bash
gcloud run services proxy "$SERVICE" \
  --project="$PROJECT_ID" --region="$REGION" --port=8081
```

В іншому терміналі:

```bash
BASE=http://127.0.0.1:8081
curl -fsS "$BASE/health"
curl -fsS "$BASE/readyz"
curl -fsS -X POST \
  "$BASE/api/apps/first_graph_agent/users/learner/sessions/cloud-demo" \
  -H 'Content-Type: application/json' -d '{}'
curl -fsS -N "$BASE/api/run_sse" \
  -H 'Content-Type: application/json' \
  -d '{"appName":"first_graph_agent","userId":"learner","sessionId":"cloud-demo","newMessage":{"role":"user","parts":[{"text":"txn-2026-07-118845 A-114"}]}}'
```

Очікуємо `OK`, `OK`, session JSON та SSE з `refund:last_case_id`.
Перший запит може стартувати новий instance. Якщо після створення сесії instance замінено, можливий 404: створіть навчальну сесію заново, не трактуйте це як гарантію збереження бізнес-операції.
Логи:

```bash
gcloud run services logs read "$SERVICE" \
  --project="$PROJECT_ID" --region="$REGION" --limit=30
```

403 означає проблему identity/IAM; це не виправляють API-ключем моделі. Перевірте активний акаунт і дайте IAM binding час поширитися.
Proxy автентифікує HTTP-виклики, тому не треба вставляти токени у файли, README чи історію команд.

## Це демо, не production deployment

`session.InMemoryService` і реєстр кейсів втрачаються при recycle, scale-to-zero та новій ревізії. Окремі HTTP-запити не мають гарантії потрапити до того самого процесу.
`--max-instances=1` і `--concurrency=1` лише зменшують варіативність демо — це **не durable storage**, не атомарний платіж і не жорстка гарантія одного процесу під час зміни ревізій.

Не запускайте load test проти хмари за замовчуванням. Нуль minimum instances не усуває витрати на запити, зберігання образів та мережу.
IAM контролює доступ до сервісу, але не авторизує `userId`/мерчанта всередині застосунку. Для кількох користувачів потрібні app-level identity/policy та спільне durable сховище для сесій **і** бізнес-стану.
Cloud Run не використовує `/readyz` автоматично лише тому, що маршрут існує; в цій вправі це діагностичний endpoint, а не налаштований платформний readiness probe.

## Прибирання після вправи

Зупиніть proxy через `Ctrl+C`. Команди нижче видаляють сервіс, **усі образи в окремому навчальному repo** та runtime account. Виконуйте їх лише для ресурсів, які щойно створили для цієї вправи; не використовуйте спільний repo чи account.

```bash
gcloud run services delete "$SERVICE" --project="$PROJECT_ID" --region="$REGION"
gcloud artifacts repositories delete "$REPOSITORY" --project="$PROJECT_ID" --location="$REGION"
gcloud iam service-accounts delete "$RUNTIME_SA@$PROJECT_ID.iam.gserviceaccount.com" --project="$PROJECT_ID"
```

Підтвердження навмисно не вимкнені. Перевірте billing: видалення сервісу не скасовує вже нараховані витрати.

## Джерела й межа перевірки

- https://adk.dev/deploy/cloud-run/
- https://cloud.google.com/run/docs/container-contract
- https://cloud.google.com/sdk/gcloud/reference/run/deploy
- https://cloud.google.com/sdk/gcloud/reference/run/services/proxy

Локально перевірені Linux/amd64 image build, non-root запуск, інший `PORT`, session/SSE API і SIGTERM. Cloud Run команди звірені з документацією; віддалений deployment у GCP не виконувався.
