package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
)

// clearEnv blanks every variable this command reads, so the tests assert the
// same program whether or not the developer has a bot token exported.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"TELEGRAM_BOT_API", "TELEGRAM_ALLOW_USER_IDS", "USER_NAME"} {
		t.Setenv(name, "")
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		allow    string
		userName string
		allowAll bool
		wantIDs  []int64
		wantErr  string
	}{
		{name: "one id", token: "t", allow: "42", wantIDs: []int64{42}},
		{name: "several ids with spaces", token: "t", allow: " 42 , 7 ", wantIDs: []int64{42, 7}},
		{name: "duplicates collapse", token: "t", allow: "42,42", wantIDs: []int64{42}},
		{name: "no token is not fatal until connect", allow: "42", wantIDs: []int64{42}},
		{name: "empty allowlist refuses to start", token: "t", wantErr: "TELEGRAM_ALLOW_USER_IDS is empty"},
		{name: "allow-all overrides the empty allowlist", token: "t", allowAll: true},
		{name: "a username is not an id", token: "t", allow: "@dimetron", wantErr: "is not a numeric user id"},
		{name: "empty item between commas is ignored", token: "t", allow: "42,,7", wantIDs: []int64{42, 7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("TELEGRAM_BOT_API", tt.token)
			t.Setenv("TELEGRAM_ALLOW_USER_IDS", tt.allow)
			t.Setenv("USER_NAME", tt.userName)

			cfg, err := loadConfig(tt.allowAll)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("loadConfig = %v, want an error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig = %v, want no error", err)
			}
			if got := len(cfg.allowed); got != len(tt.wantIDs) {
				t.Fatalf("len(allowed) = %d, want %d (%v)", got, len(tt.wantIDs), tt.wantIDs)
			}
			for _, id := range tt.wantIDs {
				if !cfg.allowed[id] {
					t.Errorf("allowed[%d] = false, want true", id)
				}
			}
		})
	}
}

func TestAllowed(t *testing.T) {
	tests := []struct {
		name    string
		allowed map[int64]bool
		userID  int64
		want    bool
	}{
		{name: "listed user", allowed: map[int64]bool{42: true}, userID: 42, want: true},
		{name: "unlisted user", allowed: map[int64]bool{42: true}, userID: 7, want: false},
		{name: "empty list is open", allowed: map[int64]bool{}, userID: 7, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &handler{cfg: config{allowed: tt.allowed}}
			if got := h.allowed(tt.userID); got != tt.want {
				t.Errorf("allowed(%d) = %v, want %v", tt.userID, got, tt.want)
			}
		})
	}
}

func TestSplitMessage(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		max   int
		want  []string
		check func(t *testing.T, got []string)
	}{
		{name: "short text is one chunk", text: "hello", max: 10, want: []string{"hello"}},
		{name: "empty text yields nothing", text: "   \n ", max: 10},
		{name: "breaks at a space", text: "aaa bbb ccc", max: 8, want: []string{"aaa bbb", "ccc"}},
		{name: "breaks at a newline", text: "one\ntwo\nthree", max: 9, want: []string{"one\ntwo", "three"}},
		{name: "unbroken run is hard-split", text: "abcdefghij", max: 4, want: []string{"abcd", "efgh", "ij"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitMessage(tt.text, tt.max)
			if tt.check != nil {
				tt.check(t, got)
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("splitMessage(%q, %d) = %q, want %q", tt.text, tt.max, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("chunk %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestSplitMessageNeverExceedsMax pins the property Telegram actually cares
// about: no chunk is over the limit, whatever the input looks like.
func TestSplitMessageNeverExceedsMax(t *testing.T) {
	text := strings.Repeat("word ", 500) + strings.Repeat("x", 300)
	for _, chunk := range splitMessage(text, 64) {
		if n := len([]rune(chunk)); n > 64 {
			t.Fatalf("chunk of %d runes exceeds the 64-rune limit: %q", n, chunk)
		}
	}
}

func TestCommand(t *testing.T) {
	tests := []struct {
		text string
		want string
		ok   bool
	}{
		{text: "/help", want: "help", ok: true},
		{text: "/HELP", want: "help", ok: true},
		{text: "/help@my_bot", want: "help", ok: true},
		{text: "/new now", want: "new", ok: true},
		{text: "hello", ok: false},
		{text: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got, ok := command(tt.text)
			if ok != tt.ok || got != tt.want {
				t.Errorf("command(%q) = (%q, %v), want (%q, %v)", tt.text, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestSessionsNext(t *testing.T) {
	s := newSessions()
	first := s.next(42, false)
	if again := s.next(42, false); again != first {
		t.Errorf("second call = %q, want the same session %q", again, first)
	}
	reset := s.next(42, true)
	if reset == first {
		t.Errorf("/new returned %q, want a different session from %q", reset, first)
	}
	if other := s.next(7, false); other == first || other == reset {
		t.Errorf("chat 7 got %q, which collides with chat 42's sessions", other)
	}
}

func TestCurrentTime(t *testing.T) {
	got, err := currentTime(nil, timeArgs{})
	if err != nil {
		t.Fatalf("currentTime(default) = %v", err)
	}
	if got.Timezone != "Europe/Kyiv" {
		t.Errorf("Timezone = %q, want the documented default Europe/Kyiv", got.Timezone)
	}
	if _, err := time.Parse("2006-01-02 15:04 MST", got.Time); err != nil {
		t.Errorf("Time = %q, which does not parse as the documented format: %v", got.Time, err)
	}
	if _, err := currentTime(nil, timeArgs{Timezone: "Mars/Olympus"}); err == nil {
		t.Error("currentTime(invalid zone) = nil error, want a failure the model can report")
	}
}

// fakeRunner records the turns a handler asked for.
type fakeRunner struct {
	mu     sync.Mutex
	turns  []string
	result labrun.Result
	err    error
}

func (f *fakeRunner) Turn(_ context.Context, _, sessionID, input string) (labrun.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.turns = append(f.turns, sessionID+"|"+input)
	return f.result, f.err
}

func (f *fakeRunner) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.turns) == 0 {
		return ""
	}
	return f.turns[len(f.turns)-1]
}

// sent records the messages a fake Telegram server received.
type sent struct {
	mu    sync.Mutex
	texts []string
	calls []string
}

func (s *sent) add(method, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, method)
	if text != "" {
		s.texts = append(s.texts, text)
	}
}

func (s *sent) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.texts...)
}

func (s *sent) methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// fakeTelegram is enough of the Bot API for the handler: it records sendMessage
// calls and answers getUpdates with nothing.
func fakeTelegram(t *testing.T) (*httptest.Server, *sent) {
	t.Helper()
	rec := &sent{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		text, _ := payload["text"].(string)
		rec.add(method, text)
		if method == "sendMessage" {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func newTestHandler(t *testing.T, cfg config, runner botRunner) (*handler, *sent) {
	t.Helper()
	srv, rec := fakeTelegram(t)
	return &handler{
		cfg:      cfg,
		bot:      newBot("test-token", srv.URL),
		runner:   runner,
		sessions: newSessions(),
	}, rec
}

func msgUpdate(userID, chatID int64, text string) update {
	u := update{UpdateID: 1}
	m := &message{MessageID: 1, Text: text, From: &user{ID: userID, Username: "tester"}}
	m.Chat.ID = chatID
	u.Message = m
	return u
}

func TestHandleRejectsDisallowedUser(t *testing.T) {
	h, rec := newTestHandler(t, config{token: "t", allowed: map[int64]bool{42: true}}, &fakeRunner{})

	h.handle(context.Background(), msgUpdate(7, 7, "hello"))

	texts := rec.all()
	if len(texts) != 1 {
		t.Fatalf("sent %d messages, want exactly one refusal", len(texts))
	}
	if !strings.Contains(texts[0], "7") {
		t.Errorf("refusal = %q, want it to name the id the owner must allowlist", texts[0])
	}
}

func TestHandleCommands(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    string
		noAgent bool
	}{
		{name: "/start greets", text: "/start", want: "ADK agent"},
		{name: "/help explains", text: "/help", want: "fresh conversation"},
		{name: "/id reports the id", text: "/id", want: "4242"},
		{name: "/new resets", text: "/new", want: "fresh conversation", noAgent: true},
		{name: "unknown command is refused", text: "/dance", want: "Unknown command", noAgent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{result: labrun.Result{Final: "agent answer"}}
			h, rec := newTestHandler(t, config{token: "t", allowed: map[int64]bool{4242: true}}, runner)

			h.handle(context.Background(), msgUpdate(4242, 999, tt.text))

			texts := rec.all()
			if len(texts) != 1 {
				t.Fatalf("sent %d messages (%q), want one", len(texts), texts)
			}
			if !strings.Contains(texts[0], tt.want) {
				t.Errorf("reply = %q, want it to contain %q", texts[0], tt.want)
			}
			if tt.noAgent && runner.last() != "" {
				t.Errorf("runner was called with %q, want a command to skip the agent", runner.last())
			}
		})
	}
}

func TestHandleAgentTurn(t *testing.T) {
	runner := &fakeRunner{result: labrun.Result{
		Final:     "It is 12:00 in Kyiv.",
		ToolCalls: []string{"current_time"},
	}}
	h, rec := newTestHandler(t, config{token: "t", allowed: map[int64]bool{4242: true}}, runner)

	h.handle(context.Background(), msgUpdate(4242, 999, "what time is it?"))

	if got := runner.last(); !strings.HasSuffix(got, "|what time is it?") {
		t.Errorf("runner saw %q, want the user's text", got)
	}
	texts := rec.all()
	if len(texts) != 1 || texts[0] != "It is 12:00 in Kyiv." {
		t.Fatalf("sent %q, want the agent's answer verbatim", texts)
	}
	if methods := rec.methods(); methods[0] != "sendChatAction" {
		t.Errorf("first Telegram call = %q, want a typing action before the answer", methods[0])
	}
}

// TestHandleTurnFailureIsReported pins that a broken model does not kill the
// bot: the user gets a message they can act on instead of silence.
func TestHandleTurnFailureIsReported(t *testing.T) {
	runner := &fakeRunner{err: errors.New("model unavailable")}
	h, rec := newTestHandler(t, config{token: "t", allowed: map[int64]bool{4242: true}}, runner)

	h.handle(context.Background(), msgUpdate(4242, 999, "hello"))

	texts := rec.all()
	if len(texts) != 1 || !strings.Contains(texts[0], "failed") {
		t.Fatalf("sent %q, want an error notice for the user", texts)
	}
}

func TestHandleIgnoresUnusableUpdates(t *testing.T) {
	t.Run("no message", func(t *testing.T) {
		h, rec := newTestHandler(t, config{token: "t"}, &fakeRunner{})
		h.handle(context.Background(), update{UpdateID: 2})
		if got := rec.all(); len(got) != 0 {
			t.Errorf("sent %q, want nothing for an update without a message", got)
		}
	})
	t.Run("no author", func(t *testing.T) {
		h, rec := newTestHandler(t, config{token: "t"}, &fakeRunner{})
		u := update{UpdateID: 2, Message: &message{Text: "hi"}}
		h.handle(context.Background(), u)
		if got := rec.all(); len(got) != 0 {
			t.Errorf("sent %q, want nothing for an authorless message", got)
		}
	})
	t.Run("empty text", func(t *testing.T) {
		h, rec := newTestHandler(t, config{token: "t", allowed: map[int64]bool{1: true}}, &fakeRunner{})
		h.handle(context.Background(), msgUpdate(1, 1, "   "))
		got := rec.all()
		if len(got) != 1 || !strings.Contains(got[0], "text messages") {
			t.Errorf("sent %q, want a note that only text is handled", got)
		}
	})
	t.Run("another bot", func(t *testing.T) {
		runner := &fakeRunner{result: labrun.Result{Final: "answer"}}
		h, rec := newTestHandler(t, config{token: "t", allowed: map[int64]bool{1: true}}, runner)
		u := msgUpdate(1, 1, "hi")
		u.Message.From.IsBot = true
		h.handle(context.Background(), u)
		if got := rec.all(); len(got) != 0 {
			t.Errorf("sent %q, want bot messages ignored (loop protection)", got)
		}
		if runner.last() != "" {
			t.Errorf("runner ran on a bot message: %q", runner.last())
		}
	})
}

// TestHandleEmptyAgentAnswer covers the one shape a scripted runner can produce
// that a real model rarely does: no text at all.
func TestHandleEmptyAgentAnswer(t *testing.T) {
	h, rec := newTestHandler(t, config{token: "t", allowed: map[int64]bool{1: true}}, &fakeRunner{})
	h.handle(context.Background(), msgUpdate(1, 1, "hello"))
	got := rec.all()
	if len(got) != 1 || !strings.Contains(got[0], "empty answer") {
		t.Errorf("sent %q, want a placeholder instead of a silent turn", got)
	}
}

// TestPollStopsOnCancellation pins the Ctrl-C path: the loop must return the
// context error instead of spinning forever.
func TestPollStopsOnCancellation(t *testing.T) {
	srv, _ := fakeTelegram(t)
	h := &handler{cfg: config{token: "t", allowed: map[int64]bool{1: true}}, bot: newBot("test-token", srv.URL), runner: &fakeRunner{}, sessions: newSessions()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := poll(ctx, h); !errors.Is(err, context.Canceled) {
		t.Fatalf("poll(cancelled ctx) = %v, want context.Canceled", err)
	}
}

// TestPollSurvivesAPollingFailure pins the retry behaviour: one bad poll is not
// the end of the bot, and the offset still advances past handled updates.
func TestPollSurvivesAPollingFailure(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if method != "getUpdates" {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		hits++
		if hits == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":5,"message":{"message_id":1,"chat":{"id":1},"from":{"id":999},"text":"hi"}}]}`))
	}))
	defer srv.Close()

	runner := &fakeRunner{result: labrun.Result{Final: "answer"}}
	h := &handler{
		cfg:          config{token: "t", allowed: map[int64]bool{999: true}},
		bot:          newBot("tok", srv.URL),
		runner:       runner,
		sessions:     newSessions(),
		retryBackoff: time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Let the failure and the following successful poll happen, then stop.
		for i := 0; i < 400 && runner.last() == ""; i++ {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()

	if err := poll(ctx, h); !errors.Is(err, context.Canceled) {
		t.Fatalf("poll = %v, want context.Canceled after the cancel", err)
	}
	if hits < 2 {
		t.Errorf("getUpdates was called %d times, want a retry after the failure", hits)
	}
	if runner.last() == "" {
		t.Error("the update after the failed poll was never handled")
	}
}

func TestBotCallErrors(t *testing.T) {
	t.Run("telegram says not ok", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
		}))
		defer srv.Close()
		b := newBot("tok", srv.URL)
		err := b.send(context.Background(), 1, "hi")
		if err == nil || !strings.Contains(err.Error(), "chat not found") {
			t.Fatalf("send = %v, want Telegram's own description", err)
		}
	})

	t.Run("rate limit carries the retry hint", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"parameters":{"retry_after":9}}`))
		}))
		defer srv.Close()
		b := newBot("tok", srv.URL)
		err := b.send(context.Background(), 1, "hi")
		var rl *rateLimitError
		if !errors.As(err, &rl) {
			t.Fatalf("send = %v, want a rateLimitError", err)
		}
		if rl.retryAfter != 9*time.Second {
			t.Errorf("retryAfter = %s, want 9s from Telegram's parameters", rl.retryAfter)
		}
	})

	t.Run("non-2xx", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusBadGateway)
		}))
		defer srv.Close()
		if _, err := newBot("tok", srv.URL).getMe(context.Background()); err == nil {
			t.Fatal("getMe = nil, want an HTTP error")
		}
	})
}

// TestNewAgentWiresTheTool is the wiring test: a real ADK agent over a scripted
// model, proving the Telegram command builds an agent that actually dispatches
// current_time. No key, no network.
func TestNewAgentWiresTheTool(t *testing.T) {
	m := fakellm.New("scripted",
		fakellm.CallTurn("current_time", map[string]any{"timezone": "Europe/Kyiv"}),
		fakellm.TextTurn("Kyiv time fetched."),
	)
	a, err := newAgent(m)
	if err != nil {
		t.Fatalf("newAgent: %v", err)
	}
	if a.Name() != "telegram_helper" {
		t.Errorf("agent name = %q, want telegram_helper", a.Name())
	}

	res, err := labrun.Run(context.Background(), a, "what time is it?")
	if err != nil {
		t.Fatalf("labrun.Run: %v", err)
	}
	if !res.CalledTool("current_time") {
		t.Errorf("tool calls = %v, want current_time", res.ToolCalls)
	}
	if res.Final != "Kyiv time fetched." {
		t.Errorf("final = %q, want the scripted answer", res.Final)
	}
}

// TestRunDryRun is the whole path minus Telegram: apps/.env, the provider, the
// agent and the config check, asserted to need no bot token.
func TestRunDryRun(t *testing.T) {
	clearEnv(t)
	t.Setenv("TELEGRAM_ALLOW_USER_IDS", "4242")

	if err := run(context.Background(), options{dryRun: true}); err != nil {
		t.Skipf("no provider configured for this environment: %v", err)
	}
}

func TestRunRequiresAToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("TELEGRAM_ALLOW_USER_IDS", "4242")

	err := run(context.Background(), options{})
	if err == nil {
		t.Fatal("run with no TELEGRAM_BOT_API = nil, want a startup failure")
	}
	// Either the provider is missing (no key in this environment) or the token
	// is — both are legitimate refusals, and neither may reach the network.
	if !strings.Contains(err.Error(), "TELEGRAM_BOT_API") && !strings.Contains(err.Error(), "apps/.env") {
		t.Errorf("run = %v, want it to name the missing credential", err)
	}
}

func TestAllowSummary(t *testing.T) {
	if got := allowSummary(nil); !strings.Contains(got, "EVERY user") {
		t.Errorf("allowSummary(empty) = %q, want a loud warning", got)
	}
	if got := allowSummary(map[int64]bool{42: true}); got != "42" {
		t.Errorf("allowSummary = %q, want 42", got)
	}
}

func TestBannerAndWelcome(t *testing.T) {
	h := &handler{cfg: config{userName: "Dmytro"}}
	if got := h.welcome(42); !strings.Contains(got, "Dmytro") || !strings.Contains(got, "42") {
		t.Errorf("welcome = %q, want the configured name and the user's id", got)
	}
	if got := (&handler{}).welcome(42); !strings.Contains(got, "there") {
		t.Errorf("welcome with no USER_NAME = %q, want a neutral greeting", got)
	}
	// banner only logs; the assertion is that it does not panic on a username-less bot.
	banner(&user{ID: 1, FirstName: "Bot"}, config{allowed: map[int64]bool{42: true}},
		modelcfg.Choice{Provider: "gemini", Model: "gemini-3.8-flash", Reason: "test"})
}
