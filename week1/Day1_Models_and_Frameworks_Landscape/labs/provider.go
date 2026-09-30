// Провайдерська логіка стартера ДЗ 1: який бекенд запускати, з якою моделлю і
// як його побудувати.
//
// Цей файл — єдине місце, де вирішується «провайдер». Тут немає ні агента, ні
// launcher-а, і це навмисно: вибір моделі — інженерне рішення з власними
// правилами (пріоритет credentials, префікси маршрутів, фолбеки), і воно
// заслуговує окремої межі, а не рядків, розкиданих по main.
//
// Що тут живе:
//
//   - таблиця «провайдер → дефолтна модель» (providerDefaults);
//   - розв'язання вибору з оточення (chooseModel) і його будівництво (LoadModel);
//   - побудова бекенда через pimodels (createModel).
//
// Чого тут немає: читання apps/.env — це робить loadEnv у main.go, бо це
// bootstrap оточення застосунку, а не властивість провайдера. Викликайте його
// ДО LoadModel, інакше ключі з файлу ще не будуть в оточенні.
//
// Перевірено проти google.golang.org/adk/v2 v2.4.0 і pi-go v0.2.3 (станом на 09/2026).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/dimetron/pi-go/pimodels"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/adkenv"
)

// providerDefault — дефолтна модель одного провайдера.
//
// Struct, а не generics: тип моделі завжди string, і параметризувати тут
// нема чого. Generics додали б параметр, який ніколи не змінюється, — це
// складніше за задачу, а не простіше (AGENTS.md §4 «clear is better than
// clever»).
type providerDefault struct {
	// Provider — значення DEFAULT_MODEL_PROVIDER і префікс для agentgateway.
	Provider string
	// Model — дефолтна модель. Для agentgateway/ollama це локальний тег, бо
	// саме локальний демон шлюз використовує як свій ollama-бекенд.
	Model string
	// EnvVar — змінна, наявність якої вмикає провайдера ("" — ключ не потрібен).
	EnvVar string
	// EnvModel — змінна для перевизначення моделі саме цього провайдера.
	EnvModel string
}

// providerDefaults — таблиця «провайдер → дефолтна модель».
//
// Три agentgateway-рядки — це три РІЗНІ маршрути одного шлюзу, і саме тому
// вони тут окремо: `agentgateway/ollama` іде на локальний демон, `-cloud`-тег
// — на api.ollama.com, а `agentgateway/openai` та `agentgateway/gemini` — у
// вендорські API. Для агента це просто імена моделей; різницю в поведінці —
// ціну, ліцензію, data residency — створює маршрут, а не код агента.
//
// Порядок важливий: agentgateway першим. Коли локальний шлюз піднято, весь
// трафік має йти крізь нього, інакше траси й облік вартості діряві й перестають
// бути доказом (та сама логіка, що в Частині 2, agent.go:BuildModel). Цей самий
// порядок — пріоритет автовизначення в autoProvider і порядок з AGENTS.md §3.
//
// Моделі — навмисно «станом на 08/2026» (AGENTS.md §3). Id, якого провайдер уже
// не обслуговує, падає гучно з його власною помилкою, а не тихо бенчмаркається
// під чужим ім'ям. Для agentgateway/ollama «плаваючого» тега не існує —
// локальні теги завжди конкретні, тому там узятий розмірний варіант, який
// влазить у пам'ять ноутбука.
var providerDefaults = []providerDefault{
	{
		Provider: "agentgateway/ollama",
		Model:    "qwen3.5:4b-mlx",
		EnvVar:   "AGENTGATEWAY_BASE_URL",
		EnvModel: "AGENTGATEWAY_MODEL",
	},
	{
		Provider: "agentgateway/openai",
		Model:    "gpt-5.6-luna",
		EnvVar:   "AGENTGATEWAY_BASE_URL",
		EnvModel: "AGENTGATEWAY_MODEL",
	},
	{
		Provider: "agentgateway/gemini",
		Model:    "gemini-3.8-flash",
		EnvVar:   "AGENTGATEWAY_BASE_URL",
		EnvModel: "AGENTGATEWAY_MODEL",
	},
	{
		Provider: "ollama",
		Model:    "deepseek-v4.1-flash:cloud",
		EnvVar:   "OLLAMA_BASE_URL",
		EnvModel: "OLLAMA_MODEL",
	},
	{
		Provider: "gemini",
		Model:    "gemini-3.8-flash",
		EnvVar:   "GOOGLE_API_KEY",
		EnvModel: "GEMINI_MODEL",
	},
	{
		Provider: "openai",
		Model:    "gpt-5.6-luna",
		EnvVar:   "OPENAI_API_KEY",
		EnvModel: "OPENAI_MODEL",
	},
}

// geminiAPIKeyAliases — імена, з яких стартер приймає ключ Gemini.
//
// adkenv.Load наповнює лише ті змінні, яких ще немає в оточенні, тож студент,
// який експортував GEMINI_API_KEY (так ця змінна зветься в .env-example і в
// pimodels.APIKeyEnvVar), інакше отримав би «провайдера не налаштовано» при
// живому ключі.
var geminiAPIKeyAliases = []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}

// modelChoice — те, що стартер вирішив запускати, і чому саме.
//
// Причина повертається назовні навмисно: той, хто бачить не ту відповідь,
// має бачити й те, який провайдер її дав.
type modelChoice struct {
	Provider string
	Model    string
	Reason   string
}

// resolveModelName повертає ім'я моделі зі змінної MODEL або дефолт.
//
// Винесено в окрему функцію рівно заради тесту: на цьому інваріанті тримається
// Завдання 5 (прогін кількох моделей зміною ОДНІЄЇ змінної). Якби MODEL
// мовчки ігнорувалась, бенчмарк порівнював би модель саму з собою й видав
// цілком правдоподібну таблицю — найгірший різновид помилки, бо нічого не падає.
//
// MODEL — це ім'я моделі ЦІЛКОМ, разом із префіксом маршруту: саме тому один
// рядок `MODEL=agentgateway/openai/gpt-5.6-luna` перемикає і провайдера, і
// модель. Другий аргумент — лише фолбек.
func resolveModelName(env, fallback string) string {
	if strings.TrimSpace(env) == "" {
		return fallback
	}
	return strings.TrimSpace(env)
}

// resolveProvider віддає рядок таблиці для провайдера (регістр не має
// значення) і повідомляє, чи такий провайдер взагалі відомий.
func resolveProvider(name string) (providerDefault, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, d := range providerDefaults {
		if d.Provider == want {
			return d, true
		}
	}
	return providerDefault{}, false
}

// providerForModel вгадує рядок таблиці за префіксом імені моделі.
//
// Це для випадку «MODEL задано, DEFAULT_MODEL_PROVIDER — ні»: префікс уже
// містить відповідь, і вимагати від студента продублювати її в другій змінній
// означало б гарантовану розбіжність між ними.
//
// Три проходи, від точнішого до загальнішого:
//
//  1. Найдовший повний збіг `provider + "/"`, щоб `agentgateway/gemini/...` не
//     зматчився як `agentgateway/ollama`. Без цього проходу Gemini-запит пішов
//     би на локальний демон Ollama й упав би з «model not found» — повідомленням,
//     яке відправляє студента шукати відсутній `ollama pull` замість помилки
//     маршрутизації.
//  2. Збіг за першим сегментом. Він потрібен для маршрутів усередині шлюзу,
//     яких немає в таблиці окремим рядком: `agentgateway/deepseek-...` — це той
//     самий шлюз, хоч у таблиці його і немає. Модель тут усе одно береться з
//     MODEL, тож рядок визначає лише сімейство маршруту.
//  3. Голий вендорський id (`gpt-5.6-luna`, `deepseek-v4.1-flash:cloud`) без
//     жодного префікса. Тут питаємо pimodels — він і є джерелом істини про
//     відповідність «ім'я → провайдер». Без цього проходу така модель дістала б
//     мітку gemini, і при заданому OLLAMA_BASE_URL ми б причепили Ollama-endpoint
//     до моделі OpenAI — помилка, яка виглядає як проблема провайдера.
func providerForModel(modelName string) (providerDefault, bool) {
	lower := strings.ToLower(strings.TrimSpace(modelName))

	var best providerDefault
	var found bool
	for _, d := range providerDefaults {
		if strings.HasPrefix(lower, d.Provider+"/") && (!found || len(d.Provider) > len(best.Provider)) {
			best, found = d, true
		}
	}
	if found {
		return best, true
	}

	segment, _, _ := strings.Cut(lower, "/")
	for _, d := range providerDefaults {
		if top, _, ok := strings.Cut(d.Provider, "/"); ok && top == segment {
			return d, true
		}
	}

	// Голий id: провайдера знає лише pimodels.
	if info, err := pimodels.Resolve(modelName); err == nil {
		if d, ok := resolveProvider(info.Provider); ok {
			return d, true
		}
	}
	return providerDefault{}, false
}

// autoProvider обирає провайдера за наявними в оточенні credentials.
//
// Порядок — це порядок providerDefaults, тобто пріоритет AGENTS.md §3:
// agentgateway → ollama → gemini → openai.
func autoProvider() (providerDefault, bool) {
	for _, d := range providerDefaults {
		if _, ok := providerCredential(d); ok {
			return d, true
		}
	}
	return providerDefault{}, false
}

// providerCredential повертає credential провайдера — ключ або базовий URL — і
// те, чи він узагалі заданий.
//
// Читаємо через adkenv.Key, а не os.Getenv: порожня, але виставлена змінна —
// типовий спосіб зламати CI, і трактувати її як «провайдер налаштовано» означає
// отримати незрозумілий 401 замість чесного «не налаштовано».
func providerCredential(d providerDefault) (string, bool) {
	if d.EnvVar == "" {
		return "", true
	}
	for _, name := range envVarAliases(d.EnvVar) {
		if v, ok := adkenv.Key(name); ok {
			return v, true
		}
	}
	return "", false
}

// envVarAliases розширює ім'я змінної провайдера до всіх його синонімів.
//
// Ключ у таблиці й ключ, який насправді читає pimodels, — різні імена
// (GOOGLE_API_KEY проти GEMINI_API_KEY), тож для Gemini перевіряються обидва.
func envVarAliases(name string) []string {
	if name == "GOOGLE_API_KEY" {
		return geminiAPIKeyAliases
	}
	return []string{name}
}

// chooseModel вирішує, якого провайдера й яку модель запускати.
//
// Порядок розв'язання:
//
//  1. DEFAULT_MODEL_PROVIDER задано — провайдер названо явно; модель із
//     <PROVIDER>_MODEL, інакше дефолт цього провайдера.
//  2. MODEL задано (а провайдер — ні) — провайдер із префікса імені моделі, а
//     якщо префікса немає, його підказує pimodels.
//  3. Нічого не задано — провайдер за наявним ключем, модель — його дефолт.
//
// Усі чотири відмови гучні: невідомий провайдер у DEFAULT_MODEL_PROVIDER,
// нерозпізнаний MODEL, і «не задано нічого». Кожна називає відомі провайдери
// (knownProviders) — щоб після помилки не доводилося шукати таблицю.
//
// Мовчазного фолбека на gemini тут немає навмисно. Раніше студент без жодного
// ключа отримував «gemini → gemini-3.8-flash» у лозі й падіння аж на першому
// запиті — з помилкою чужого провайдера, за якою не видно причини. Провайдер,
// якого ніхто не вибирав, — це не дефолт, а здогад; здогад мусить бути видимим
// (той самий принцип, що й у невідомого DEFAULT_MODEL_PROVIDER нижче).
func chooseModel(modelEnv, providerEnv string) (modelChoice, error) {
	if provider := strings.TrimSpace(providerEnv); provider != "" {
		d, ok := resolveProvider(provider)
		if !ok {
			return modelChoice{}, fmt.Errorf("невідомий провайдер %q у DEFAULT_MODEL_PROVIDER; відомі: %s",
				provider, knownProviders())
		}
		return choiceFor(d, modelEnv), nil
	}

	if model := strings.TrimSpace(modelEnv); model != "" {
		fromPrefix, ok := providerForModel(model)
		if !ok {
			return modelChoice{}, fmt.Errorf(
				"не вдалося визначити провайдера для MODEL=%q: ні префікс маршруту, ні pimodels його не знають.\n"+
					"Допишіть маршрут у MODEL (напр. MODEL=ollama/%s) або задайте DEFAULT_MODEL_PROVIDER.\n"+
					"Відомі провайдери: %s",
				model, model, knownProviders())
		}
		return choiceFor(fromPrefix, model), nil
	}

	d, ok := autoProvider()
	if !ok {
		return modelChoice{}, fmt.Errorf(
			"жодного провайдера не налаштовано: немає ні ключа, ні MODEL, ні DEFAULT_MODEL_PROVIDER.\n"+
				"Впишіть ключ у apps/.env (шаблон — apps/.env-example) або задайте DEFAULT_MODEL_PROVIDER.\n"+
				"Відомі провайдери: %s",
			knownProviders())
	}
	return choiceFor(d, ""), nil
}

// choiceFor заповнює вибір.
//
// Пріоритет джерел моделі: спочатку аргумент (MODEL або явне значення виклику),
// потім провайдерська змінна (<PROVIDER>_MODEL), потім дефолт таблиці.
// Провайдерська змінна сильніша за дефолт, бо вона специфічніша: студент, який
// виставив OLLAMA_MODEL, назвав модель точніше, ніж таблиця.
func choiceFor(d providerDefault, modelEnv string) modelChoice {
	model := strings.TrimSpace(modelEnv)
	if model == "" && d.EnvModel != "" {
		if v, ok := adkenv.Key(d.EnvModel); ok {
			model = v
		}
	}
	model = qualifyGatewayModel(d.Provider, resolveModelName(model, d.Model))
	return modelChoice{
		Provider: d.Provider,
		Model:    model,
		Reason:   fmt.Sprintf("%s → %s", d.Provider, model),
	}
}

// qualifyGatewayModel добудовує маршрут шлюзу в ім'я моделі, коли провайдер —
// agentgateway, а ім'я — голе.
//
// Це не косметика, а сама маршрутизація: pimodels вирішує «шлюз чи вендор»
// ВИКЛЮЧНО за префіксом імені. Голе `gemini-3.8-flash` при провайдері
// `agentgateway/gemini` раніше йшло в pimodels як є — і резолвилося в прямий
// Gemini-клієнт до Google, тихо обминаючи шлюз. Назовні це виглядало як
// «через шлюз grounding не працює», хоча запит шлюзу навіть не торкався;
// траси й облік вартості при цьому теж діряві (той самий інваріант, що в
// коментарі до providerDefaults: піднятий шлюз має бачити весь трафік).
//
// Правила добудови — від найточнішого збігу до повного:
//
//   - ім'я вже несе `agentgateway/` — не чіпаємо (MODEL задано цілком);
//   - ім'я вже несе вендорський сегмент маршруту (`gemini/...`) — досить
//     докласти `agentgateway/`, інакше вийшов би подвоєний сегмент;
//   - голе ім'я — докладаємо весь маршрут (`agentgateway/gemini/`).
func qualifyGatewayModel(provider, model string) string {
	if !isAgentGateway(provider) {
		return model
	}
	lower := strings.ToLower(model)
	if strings.HasPrefix(lower, "agentgateway/") {
		return model
	}
	if vendor, ok := strings.CutPrefix(strings.ToLower(provider), "agentgateway/"); ok &&
		strings.HasPrefix(lower, vendor+"/") {
		return "agentgateway/" + model
	}
	return provider + "/" + model
}

// knownProviders збирає перелік провайдерів для повідомлення про помилку.
func knownProviders() string {
	names := make([]string, 0, len(providerDefaults))
	for _, d := range providerDefaults {
		names = append(names, d.Provider)
	}
	return strings.Join(names, ", ")
}

// createModel instantiates an ADK model using github.com/dimetron/pi-go/pimodels.
// It resolves models across Gemini, OpenAI, Ollama, Claude, etc., handling API keys
// and base URLs automatically.
//
// Два випадки, які треба розрізняти:
//
//   - `agentgateway/<vendor>/<model>` — це маршрут шлюзу, а не вендорський API.
//     Endpoint тут — сам шлюз: AGENTGATEWAY_BASE_URL, якщо задано, інакше
//     дефолт pimodels (http://localhost:4000). OLLAMA_BASE_URL сюди НЕ
//     передається: це адреса іншого сервіса, і вона перебила б адресу шлюзу
//     та зламала б маршрутизацію всередині нього.
//   - будь-яка інша модель плюс OLLAMA_BASE_URL — це вказівка на конкретний
//     endpoint, і вона передається явно.
//
// Ретрай з префіксом `ollama/` умовний, і умова тут — суть. Він існує для
// голого локального тега (`qwen3.5:4b-mlx`), який pimodels не впізнає взагалі.
// Раніше він спрацьовував на БУДЬ-ЯКУ помилку, і це робило його генератором
// фальшивих успіхів: `ollama/` не потребує ключа й нічого не викликає, тож
// модель будувалася завжди, а справжня причина — «api key is required» —
// зникала. Назовні це виглядало як чужий 404 на першому запиті. Перевірка
// Resolve повертає рівно те, для чого ретрай писався.
func createModel(ctx context.Context, provider, modelName string) (model.LLM, error) {
	var opts []pimodels.Option
	if isAgentGateway(provider) {
		// Адресу шлюзу треба передати явно: pimodels цю змінну як endpoint не
		// читає, а без неї шлюз на нестандартному порту тихо ігнорувався б —
		// клієнт стукав би в дефолтний :4000. Читання через adkenv.Key, щоб
		// порожня-але-виставлена змінна не затерла дефолт pimodels.
		if base, ok := adkenv.Key("AGENTGATEWAY_BASE_URL"); ok {
			opts = append(opts, pimodels.WithBaseURL(base))
		}
	} else if base := os.Getenv("OLLAMA_BASE_URL"); base != "" {
		opts = append(opts, pimodels.WithBaseURL(base))
	}
	m, err := pimodels.New(ctx, modelName, opts...)
	if err != nil {
		// Prefixing only helps a name pimodels cannot route at all. When it
		// CAN route the name, the failure is real — a missing credential or a
		// bad endpoint — and re-asking as ollama/<name> would replace it with a
		// successful build of a model nobody asked for.
		if _, resolveErr := pimodels.Resolve(modelName); resolveErr == nil {
			return nil, err
		}
		if mOllama, errOllama := pimodels.New(ctx, "ollama/"+modelName, opts...); errOllama == nil {
			return mOllama, nil
		}
		return nil, err
	}
	return m, nil
}

// isAgentGateway повідомляє, чи провайдер — це маршрут локального шлюзу.
func isAgentGateway(provider string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(provider)), "agentgateway")
}

// LoadModel — єдина точка входу для main: визначає провайдера й модель зі
// змінних MODEL і DEFAULT_MODEL_PROVIDER, будує бекенд і повертає вибір разом
// із моделлю.
//
// Помилка будівництва збагачена підказкою про потрібну змінну: без неї
// студент бачить «API key not valid» і не знає, який саме ключ провайдер
// шукав. Це єдине місце, де помилка pimodels зустрічається з назвою змінної,
// тож підказка живе тут, а не в main.
//
// Передумова: loadEnv уже викликано — інакше ключі з apps/.env не потрапили в
// оточення, і автовизначення провайдера їх не побачить.
func LoadModel(ctx context.Context) (model.LLM, modelChoice, error) {
	choice, err := chooseModel(os.Getenv("MODEL"), os.Getenv("DEFAULT_MODEL_PROVIDER"))
	if err != nil {
		return nil, modelChoice{}, err
	}

	m, err := createModel(ctx, choice.Provider, choice.Model)
	if err != nil {
		return nil, choice, fmt.Errorf(
			"не вдалося створити модель %q (провайдер %s): %w\n"+
				"Перевірте %s в apps/.env або в оточенні",
			choice.Model, choice.Provider, err, pimodels.APIKeyEnvVar(choice.Provider))
	}
	return m, choice, nil
}
