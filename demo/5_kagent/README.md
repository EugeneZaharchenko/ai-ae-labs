# kagent: локальна збірка й встановлення

> **Перевірено наживо** (2026-10-01, macOS arm64): скрипт пройшов усі 10 кроків за
> **15 хв 23 с**, агент `assistant` отримав `Ready=True`, UI віддає `200` на
> `:8080`, контролер — на `:8083`.

[**kagent**](https://github.com/kagent-dev/kagent) — Kubernetes-нативна
control-plane для агентів: `Agent`, `Harness` і `AgentTemplate` є CRD
(`api.kagent.dev/v1alpha3`), сесії живуть у PostgreSQL і віддаються через gRPC, а
код агента виконується на **Agent Substrate**.

Ця тека не містить джерел kagent — це **окремий репозиторій**. Тут описано, як
зібрати й підняти його локально: кластер Kind, сам kagent **з вашого чекауту** й
один готовий агент, із яким можна одразу поговорити.

---

## Передумови

Скрипт збірки викликає ці інструменти (усі мають бути в `PATH`):

| Інструмент | Навіщо |
|---|---|
| `docker` + Buildx | збірка образів; на macOS — OrbStack або Docker Desktop |
| `kind` | кластер (типово `kagent`, k8s v1.35.0) на одному вузлі |
| `kubectl` | доступ до кластера |
| `helm` | встановлення `kagent`, `kagent-crds` і `substrate` |
| `jq` | розбір CA-пулів і `openid-configuration` у кроці 5 |
| `openssl` | конвертація кореневого сертифіката з DER у PEM |
| `curl`, `base64` | завантаження `kubectl-ate` і кодування секретів |
| `make` | цілі `create-kind-cluster` та `helm-install` усередині скрипта |
| **Go 1.27+** | `make build` генерує protobuf через `go run …/buf` — **на хості**, не в контейнері |
| `yarn` | лише для `yarn dev` (розробницький цикл), не для самого скрипта |

Мінімум місця на диску — **15–20 GiB**: самі лише образи займають ~7 GB, а
Docker-томи (реєстр, Postgres, snapshots) — ще десятки. На цьому прогоні вільне
місце впало з 25 GiB до 9 GiB.

Dev container курсу (`.devcontainer/`) уже містить `kind`, `kubectl`, `helm` і
Docker-in-Docker, тож у ньому з передумов лишаються тільки `jq` та `openssl`.

---

## Швидкий старт

```bash
git clone https://github.com/kagent-dev/kagent.git
cd kagent
./scripts/setup-cluster/setup-cluster.sh
```

Скрипт ідемпотентний: повторний запуск оновлює наявний кластер, а не створює
другий. Після завершення він **лишає відкритими два port-forward** і чекає на них
у тому ж шелі, тож не закривайте термінал (Ctrl-C зупинить обидва).

---

## Що робить скрипт

Порядок важливий: кожне навантаження Substrate монтує секрети, яких ще немає,
доки не виконано команди `kubectl-ate`. Саме тому це скрипт, а не чотири команди.

| # | Крок | Що з'являється |
|---|---|---|
| 1 | `make create-kind-cluster` | кластер Kind `kagent` + локальний реєстр на `:5001` + MetalLB |
| 2 | завантаження `kubectl-ate` | CLI (на хост, не в кластер), який карбує CA/JWT-пули |
| 3 | `helm upgrade --install substrate{,-crds}` | namespace `ate-system`, v0.3.0-alpha3 |
| 4 | `kubectl-ate admin make-…` | 5 секретів-пулів (CA, JWT, egress-mitm) |
| 5 | Secret + ConfigMap | `actor-id-ca-certs`, `ate-api-authentication` |
| 6 | `make helm-install` | kagent + kagent-crds, контролер/UI/Postgres/tools/kmcp |
| 7 | `docker buildx build --push` | **контролер і UI з вашого чекауту** замість опублікованих образів |
| 8 | збірка `golang-adk` | образ середовища виконання, закріплений **за digest** |
| 9 | `kubectl apply` | агент `assistant` (inline template + Harness `kagent`) |
| 10 | `kubectl get pods` | зведення + два port-forward |

Два кроки варті окремої уваги:

- **Крок 7 перезаписує образи.** Чарт ставить опубліковані збірки, тож без цього
  кроку кластер запускав би чужий код, а виглядало б усе встановленим і здоровим.
  Після кроку 7 `kagent-controller` і `kagent-ui` — це `localhost:5001/…:dev`.
- **Крок 8 ставить Go ADK, а не Python.** Агент стартує з відновлення golden
  snapshot, і Python-рантайм цього не переживає — вертається з `Fatal Python error:
  Illegal instruction` і ніколи не віддає `/readyz`. Статичний Go-бінарник
  відновлюється чисто. Substrate вимагає digest, а digest дає лише реєстр, тому
  образ **пуш-иться**, а не завантажується в кластер.

---

## Куди дивитися

| Що | Адреса | Примітка |
|---|---|---|
| UI | **http://localhost:8080** | образ зібрано з вашого чекауту, віддає nginx |
| Контролер API | `localhost:8083` | те, куди типово дивиться `yarn dev` |
| Dev-сервер UI | http://localhost:8001 | `cd ui && yarn dev`, гаряче перезавантаження |
| Реєстр | `localhost:5001` | образи з кроку 7 |

Скрипт лишає на кластері один агент — `assistant` на harness `kagent`, щоб у
розділі **Agents** було з чим почати розмову. Готовність агента означає, що
Substrate завантажив golden actor і зняв снапшот; це триває хвилину-дві.

Перевірити стан:

```bash
kubectl --context kind-kagent get agent -n kagent
kubectl --context kind-kagent get pods -n kagent
```

```
NAME        READY   AGE
assistant   True    2m22s
```

---

## Провайдер і ключі

Типово чарт ставить провайдера **`openAI`**, і без ключа встановлення **падає** на
перевірці `check-api-key`:

```
Error: OPENAI_API_KEY environment variable is not set for OpenAI provider
```

Тому або задайте ключ, або змініть провайдера. Ключ потрібен рівно один — той,
що відповідає `KAGENT_DEFAULT_MODEL_PROVIDER`:

| Провайдер | Змінна | Типово |
|---|---|---|
| `openAI` | `OPENAI_API_KEY` | **так** — ключ обов'язковий |
| `anthropic` | `ANTHROPIC_API_KEY` | |
| `azureOpenAI` | `AZURE_OPENAI_API_KEY` | |
| `gemini` | `GOOGLE_API_KEY` | |
| `ollama` | не потрібен | локальній моделі ключ не потрібен |

```bash
export OPENAI_API_KEY=…                                  # типовий шлях
# або без ключа — на локальному Ollama:
KAGENT_DEFAULT_MODEL_PROVIDER=ollama ./scripts/setup-cluster/setup-cluster.sh
```

Модель агента береться з `ModelConfig` кластера. Після встановлення там типово
`Ollama / llama3.2`; подивитися й замінити:

```bash
kubectl --context kind-kagent get modelconfig -n kagent
```

> **Секрети в README не записуйте.** Кореневий `.env` репозиторію kagent
> Makefile підхоплює через `-include .env`, і він у `.gitignore` — ключі
> тримайте там, а не в текстах.

---

## Розробницький цикл

Після першого запуску кластер уже стоїть, і для зміни коду перезбирати все не
треба.

**UI з гарячим перезавантаженням** (кращий цикл, поки правите фронтенд):

```bash
cd ui && yarn dev            # http://localhost:8001, контролер уже проксійовано
```

**Один образ** — перезібрати й підмінити, коли треба саме той шлях, що піде в реліз
(Dockerfile, nginx, `scripts/init.sh` dev-сервер не покриває):

```bash
docker buildx build --push --platform linux/arm64 \
  -t localhost:5001/kagent-dev/kagent/ui:dev -f ui/Dockerfile ./ui
kubectl -n kagent rollout restart deploy/kagent-ui
```

Архітектура мусить збігатися з машиною: образи виконуються на вузлі Kind, а той є
контейнером на вашому хості.

---

## Чому не `make helm-install` напряму

Очевидний шлях `make create-kind-cluster && make helm-install` **не працює**, і
падає неочевидно. На свіжому кластері він спотикається об Substrate:

- **Substrate — єдиний бекенд виконання**, і чарт kagent за замовчуванням тримає
  `substrate.enabled: false` та `substrateWorkerPool.create: false` — зате
  коментує, що Substrate «requires `ate-system` installed». Без кроків 3–5 CRD
  WorkerPool не існує, і встановлення не проходить.
- **`kubectl-ate` виходить із кодом 0 трохи раніше**, ніж його секрет стає
  читабельним. Тому крок 4 чекає на сам секрет, а не довіряє коду виходу: інакше
  наступний крок монтує його й падає на щойно «створеному» кластері.

`make helm-install` лишається корисним усередині скрипта (крок 6) — він ставить
сам kagent, коли Substrate вже готовий.

---

## Прибирання

```bash
kind delete cluster --name kagent
docker rm -f $(docker ps -aq)
docker volume prune -f
```

Це звільняє основну частину місця. Образи в реєстрі на `:5001` лишаються — вони
переживуть перезапуск і пришвидшать наступний прогін.

---

## Коли щось не так

| Симптом | Причина й ліки |
|---|---|
| `Error: OPENAI_API_KEY …` | не задано ключ для типового провайдера — див. вище |
| Поди `Running 1/1`, але розмова падає | контролер крашиться через відсутні секрети Substrate; переконайтеся, що кроки 3–5 пройшли |
| Порожній список агентів або читання падають | контролер перезапустився й port-forward `:8083` умер — підніміть знову |
| `address already in use` на `:8080` | лишився старий `kubectl port-forward`; знайдіть і вбийте процес |
| `no space left on device` | приберіть (розділ вище) і повторіть |
| Агент не готовий довго | Substrate завантажує golden actor і знімає снапшот; це хвилина-дві, дивіть події агента |

Діагностика:

```bash
kubectl --context kind-kagent describe pod -n kagent <pod>
kubectl --context kind-kagent logs -n kagent <pod>
kubectl --context kind-kagent get agent -n kagent assistant -o yaml
```

Скрипт має власний докладніший README — `scripts/setup-cluster/README.md` у
репозиторії kagent.

---

## Джерела

- [github.com/kagent-dev/kagent](https://github.com/kagent-dev/kagent) — джерела проєкту
- [`docs/architecture`](https://github.com/kagent-dev/kagent/tree/main/docs/architecture) — поточна архітектура (CRD, Substrate, A2A)
- `scripts/setup-cluster/README.md` — той самий шлях із боку розробника kagent
- [Agent Substrate](https://github.com/kagent-dev/substrate) — бекенд виконання й чарти

---

Станом на 10/2026 · kagent `v1.0.0-alpha6` · Substrate `v0.3.0-alpha3` · Kind `k8s v1.35.0` · Go 1.27.1
