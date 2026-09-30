// Регресійний тест дебаг-кейсу «GoogleSearch губиться за agentgateway».
//
// Симптом: напряму з Gemini geminitool.GoogleSearch{} дає grounding, а через
// шлюз — ні. Причина була в ПРОТОКОЛІ клієнта: OpenAI-сумісний формат не
// виражає вбудовані серверні інструменти Gemini, і конвертер OpenAI→native
// відкидає {"type":"google_search"} мовчки. Лікування — нативний клієнт:
// pimodels для маршруту agentgateway/gemini/* будує genai-клієнт, який кличе
// :generateContent прямо на шлюзі (pi-go: GatewayRoutesToGemini).
//
// Цей тест пінить обидві половини контракту з боку клієнта:
//
//  1. запит іде НАТИВНИМ шляхом (…:generateContent), а не /chat/completions;
//  2. у тілі запиту інструмент живий: "googleSearch" присутній.
//
// Якщо тест червоніє після оновлення pi-go — маршрут знову їде OpenAI-виром, і
// grounding через шлюз тихо зникне; це саме та регресія, яку тут упіймано.
package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dimetron/pi-go/pimodels"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/geminitool"
	"google.golang.org/genai"
)

// capturedRequest — те, що фейковий шлюз побачив від клієнта.
type capturedRequest struct {
	Method string
	Path   string
	Auth   string
	Body   string
}

// fakeGateway піднімає сервер, який відповідає валідним JSON обом протоколам
// (native і chat/completions) і записує кожен запит. Відповідати треба обом:
// тест мусить ДОВЕСТИ, який шлях обрав клієнт, а не впасти на 404 до асершнів.
func fakeGateway(t *testing.T) (*httptest.Server, func() []capturedRequest) {
	t.Helper()

	var mu sync.Mutex
	var captured []capturedRequest

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = append(captured, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Body:   string(body),
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, ":generateContent"):
			io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"У суботу в Києві фестиваль."}]},"finishReason":"STOP"}]}`)
		case strings.Contains(r.URL.Path, "chat/completions"):
			io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"вигадана відповідь без grounding"},"finish_reason":"stop"}]}`)
		default:
			http.Error(w, `{"error":"unexpected path"}`, http.StatusNotFound)
		}
	})

	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping: local listener unavailable: %v", err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)

	return srv, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedRequest(nil), captured...)
	}
}

func TestAgentGatewayGeminiKeepsGoogleSearch(t *testing.T) {
	srv, requests := fakeGateway(t)

	// Ключ шлюзу — та сама змінна, що в лабі; перевіряємо, що він їде
	// заголовком Authorization (політика apiKey шлюзу читає саме його).
	t.Setenv("AGENTGATEWAY_API_KEY", "agw_sk_test")
	// Порожній GEMINI_API_KEY: модельний ключ підставляє сам шлюз, клієнту
	// досить плейсхолдера — тест пінить, що без вендорського ключа збірка живе.
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	ctx := context.Background()

	m, err := pimodels.New(ctx, "agentgateway/gemini/gemini-3.8-flash",
		pimodels.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("pimodels.New: %v", err)
	}

	a, err := llmagent.New(llmagent.Config{
		Name:        "weekend_planner",
		Model:       m,
		Instruction: "Ти — планувальник вихідних.",
		Tools:       []tool.Tool{geminitool.GoogleSearch{}},
	})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}

	r, err := runner.New(runner.Config{
		AppName:           "grounding-test",
		Agent:             a,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}

	msg := genai.NewContentFromText("Що робити у Києві на вихідних?", genai.RoleUser)
	for event, runErr := range r.Run(ctx, "u", "s", msg, agent.RunConfig{}) {
		if runErr != nil {
			t.Fatalf("run: %v", runErr)
		}
		_ = event
	}

	got := requests()
	if len(got) == 0 {
		t.Fatal("шлюз не отримав жодного запиту")
	}

	for _, req := range got {
		t.Logf("запит: %s %s", req.Method, req.Path)

		if strings.Contains(req.Path, "chat/completions") {
			t.Errorf("клієнт пішов OpenAI-виром (%s): цей формат мовчки губить googleSearch", req.Path)
		}
		if !strings.Contains(req.Path, ":generateContent") {
			t.Errorf("очікувався нативний шлях …:generateContent, отримано %s", req.Path)
		}
		if !strings.Contains(req.Body, "googleSearch") {
			t.Errorf("у тілі запиту немає googleSearch — інструмент загублено:\n%s", req.Body)
		}
		if want := "Bearer agw_sk_test"; req.Auth != want {
			t.Errorf("Authorization = %q, очікувалось %q (ключ шлюзу)", req.Auth, want)
		}
	}
}

// TestLabPathToGatewayKeepsGoogleSearch пінить той самий контракт крізь ПОВНИЙ
// шлях лаби: оточення → LoadModel → llmagent → runner. Це регресія на два тихі
// обходи, які виглядали однаково («через шлюз grounding не працює»):
//
//  1. голе ім'я моделі при DEFAULT_MODEL_PROVIDER=agentgateway/gemini
//     резолвилося в прямий Gemini-клієнт до Google — шлюз узагалі не бачив
//     запиту (лікує qualifyGatewayModel);
//  2. AGENTGATEWAY_BASE_URL ніде не передавався як endpoint — клієнт стукав у
//     дефолтний :4000 незалежно від змінної (лікує createModel).
func TestLabPathToGatewayKeepsGoogleSearch(t *testing.T) {
	srv, requests := fakeGateway(t)

	clearCredentials(t)
	t.Setenv("DEFAULT_MODEL_PROVIDER", "agentgateway/gemini")
	t.Setenv("AGENTGATEWAY_BASE_URL", srv.URL)
	t.Setenv("AGENTGATEWAY_API_KEY", "agw_sk_test")

	ctx := context.Background()

	m, choice, err := LoadModel(ctx)
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	if want := "agentgateway/gemini/gemini-3.8-flash"; choice.Model != want {
		t.Fatalf("choice.Model = %q, want %q (голе ім'я обминуло б шлюз)", choice.Model, want)
	}

	a, err := llmagent.New(llmagent.Config{
		Name:        "weekend_planner",
		Model:       m,
		Instruction: "Ти — планувальник вихідних.",
		Tools:       []tool.Tool{geminitool.GoogleSearch{}},
	})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}

	r, err := runner.New(runner.Config{
		AppName:           "grounding-lab-path",
		Agent:             a,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}

	msg := genai.NewContentFromText("Що робити у Києві на вихідних?", genai.RoleUser)
	for _, runErr := range r.Run(ctx, "u", "s", msg, agent.RunConfig{}) {
		if runErr != nil {
			t.Fatalf("run: %v", runErr)
		}
	}

	got := requests()
	if len(got) == 0 {
		t.Fatal("шлюз не отримав жодного запиту — трафік пішов повз AGENTGATEWAY_BASE_URL")
	}
	for _, req := range got {
		t.Logf("запит: %s %s", req.Method, req.Path)
		if !strings.Contains(req.Path, ":generateContent") {
			t.Errorf("очікувався нативний шлях …:generateContent, отримано %s", req.Path)
		}
		if !strings.Contains(req.Body, "googleSearch") {
			t.Errorf("у тілі запиту немає googleSearch — інструмент загублено:\n%s", req.Body)
		}
	}
}
