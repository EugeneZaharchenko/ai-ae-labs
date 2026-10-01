// Command 6_adk_telegram_bot is a minimal Telegram front-end for a Google ADK
// Go v2 agent.
//
// It long-polls the Telegram Bot API, sends every message from an allowed user
// to one llmagent, and posts the answer back. The provider and the model come
// from apps/.env through internal/modelcfg, exactly as the week 2 labs do, so
// there is nothing to configure in the code. One ADK session per chat keeps the
// conversation; /new starts a fresh one.
//
// Usage:
//
//	go run .            # poll Telegram until Ctrl-C
//	go run . -dry-run   # resolve config + build the agent, then exit
//
// Requires TELEGRAM_BOT_API and TELEGRAM_ALLOW_USER_IDS in apps/.env (the
// allowlist is an access control, not a preference: an empty one would answer
// any stranger with a paid model, so the bot refuses to start without it unless
// you pass -allow-all).
//
// Verified against google.golang.org/adk/v2 v2.5.0 (станом на 09/2026).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/dimetron/ai-eng-course/labs/internal/adkenv"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
)

const (
	// telegramMaxChars is Telegram's hard limit for one message, with headroom
	// for the counters the client adds.
	telegramMaxChars = 4000
	// pollTimeout is the server-side long-poll window in seconds. The HTTP
	// client waits longer than this, otherwise every healthy poll would look
	// like a timeout.
	pollTimeout    = 30
	pollHTTPWait   = 40 * time.Second
	backoffOnError = 3 * time.Second
)

// options is everything the command line can change.
type options struct {
	dryRun   bool
	allowAll bool
	apiBase  string
}

// config is the resolved bot configuration.
type config struct {
	token    string
	allowed  map[int64]bool
	userName string
}

// loadConfig reads the bot settings from the environment. apps/.env is already
// loaded into it by modelcfg.LoadEnv, so an explicit export still wins.
//
// The allowlist is parsed here and not at the point of use, so a typo in
// TELEGRAM_ALLOW_USER_IDS stops the process at startup with the offending item
// named, instead of silently letting everyone in.
func loadConfig(allowAll bool) (config, error) {
	cfg := config{
		allowed:  map[int64]bool{},
		userName: strings.TrimSpace(os.Getenv("USER_NAME")),
	}
	cfg.token, _ = adkenv.Key("TELEGRAM_BOT_API")

	raw := strings.TrimSpace(os.Getenv("TELEGRAM_ALLOW_USER_IDS"))
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		id, err := strconv.ParseInt(item, 10, 64)
		if err != nil {
			return config{}, fmt.Errorf("TELEGRAM_ALLOW_USER_IDS: %q is not a numeric user id: %w", item, err)
		}
		cfg.allowed[id] = true
	}
	if len(cfg.allowed) == 0 && !allowAll {
		return config{}, errors.New(
			"TELEGRAM_ALLOW_USER_IDS is empty in apps/.env: set your numeric id " +
				"(message @userinfobot, or start the bot and read the id from its refusal), " +
				"or pass -allow-all to deliberately serve every user")
	}
	return cfg, nil
}

// newAgent builds the one agent this bot serves.
//
// The instruction is deliberately short: this demo is about the Telegram
// plumbing, not about prompt engineering. The time tool is there so a reply can
// prove that tool dispatch happened end to end, which is the part that a chat
// front-end can silently break.
func newAgent(m model.LLM) (agent.Agent, error) {
	timeTool, err := functiontool.New(functiontool.Config{
		Name:        "current_time",
		Description: "Current date and time in an IANA timezone (default Europe/Kyiv).",
	}, currentTime)
	if err != nil {
		return nil, fmt.Errorf("build current_time tool: %w", err)
	}

	a, err := llmagent.New(llmagent.Config{
		Name:        "telegram_helper",
		Description: "A concise assistant that answers in a Telegram chat.",
		Model:       m,
		Instruction: `You are a concise assistant in a Telegram chat.
Answer in the language the user writes in.
Call current_time whenever the user asks about the time or the date; never guess them.
Keep replies under 300 characters unless the user asks for detail.`,
		Tools: []tool.Tool{timeTool},
	})
	if err != nil {
		return nil, fmt.Errorf("build agent: %w", err)
	}
	return a, nil
}

type timeArgs struct {
	Timezone string `json:"timezone" jsonschema:"IANA timezone name, e.g. Europe/Kyiv"`
}

type timeResult struct {
	Timezone string `json:"timezone"`
	Time     string `json:"time"`
	Weekday  string `json:"weekday"`
}

// currentTime is the agent's only tool: no network, no fixtures, and its result
// is impossible to invent, so a wrong answer is visibly a wrong answer.
func currentTime(_ agent.Context, in timeArgs) (timeResult, error) {
	name := strings.TrimSpace(in.Timezone)
	if name == "" {
		name = "Europe/Kyiv"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return timeResult{}, fmt.Errorf("unknown timezone %q: %w", name, err)
	}
	now := time.Now().In(loc)
	return timeResult{
		Timezone: name,
		Time:     now.Format("2006-01-02 15:04 MST"),
		Weekday:  now.Weekday().String(),
	}, nil
}

// --- Telegram wire types (only the fields this bot reads) --------------------

type apiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description,omitempty"`
	Parameters  struct {
		RetryAfter int `json:"retry_after,omitempty"`
	} `json:"parameters,omitempty"`
}

type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type message struct {
	MessageID int64  `json:"message_id"`
	Chat      chat   `json:"chat"`
	From      *user  `json:"from"`
	Text      string `json:"text"`
	Caption   string `json:"caption"`
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

// rateLimitError carries Telegram's own retry hint, which is authoritative: on
// 429 the API says exactly how long to wait.
type rateLimitError struct {
	retryAfter time.Duration
	body       string
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("telegram rate limited, retry after %s: %s", e.retryAfter, e.body)
}

// bot is the thin Telegram Bot API client this demo needs.
type bot struct {
	token   string
	apiBase string // empty means https://api.telegram.org; overridable in tests
	http    *http.Client
}

func newBot(token, apiBase string) *bot {
	return &bot{
		token:   token,
		apiBase: strings.TrimRight(apiBase, "/"),
		http:    &http.Client{Timeout: pollHTTPWait},
	}
}

func (b *bot) endpoint(method string) string {
	base := b.apiBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	return base + "/bot" + b.token + "/" + method
}

func (b *bot) call(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()

	// Cap the read: a failing Telegram response is small, and a surprise large
	// body should not be able to exhaust memory here.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%s: read response: %w", method, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		var r apiResponse[json.RawMessage]
		_ = json.Unmarshal(raw, &r)
		wait := time.Duration(r.Parameters.RetryAfter) * time.Second
		if wait <= 0 {
			wait = backoffOnError
		}
		return &rateLimitError{retryAfter: wait, body: string(raw)}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, raw)
	}

	var envelope apiResponse[json.RawMessage]
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("%s: decode envelope: %w", method, err)
	}
	if !envelope.OK {
		return fmt.Errorf("%s: telegram said %q", method, envelope.Description)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("%s: decode result: %w", method, err)
	}
	return nil
}

func (b *bot) getMe(ctx context.Context) (*user, error) {
	var me user
	if err := b.call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return nil, err
	}
	return &me, nil
}

// getUpdates blocks server-side for pollTimeout seconds, so a quiet chat costs
// one request every 30 s rather than a busy loop.
func (b *bot) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	var updates []update
	err := b.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         pollTimeout,
		"allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}

// send posts a message, splitting it so nothing is lost to Telegram's length
// limit. Errors are returned but callers normally only log them: a failed send
// must not kill the poll loop.
func (b *bot) send(ctx context.Context, chatID int64, text string) error {
	for _, chunk := range splitMessage(text, telegramMaxChars) {
		err := b.call(ctx, "sendMessage", map[string]any{
			"chat_id":                  chatID,
			"text":                     chunk,
			"disable_web_page_preview": true,
		}, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *bot) typing(ctx context.Context, chatID int64) {
	// Best effort: a chat action is cosmetic, and Telegram rate-limits them.
	_ = b.call(ctx, "sendChatAction", map[string]any{
		"chat_id": chatID,
		"action":  "typing",
	}, nil)
}

// splitMessage cuts text into chunks of at most max runes, preferring to break
// at the last newline or space so words and code blocks stay readable.
func splitMessage(text string, max int) []string {
	if max <= 0 {
		max = telegramMaxChars
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var chunks []string
	rest := []rune(text)
	for len(rest) > max {
		cut := lastBreak(rest[:max])
		if cut == 0 {
			cut = max // one unbroken run longer than a message: hard split
		}
		chunks = append(chunks, strings.TrimRight(string(rest[:cut]), " \t\r\n"))
		rest = []rune(strings.TrimLeft(string(rest[cut:]), " \t\r\n"))
	}
	if tail := strings.TrimSpace(string(rest)); tail != "" {
		chunks = append(chunks, tail)
	}
	return chunks
}

// lastBreak returns the index just past the last whitespace in r, or 0 when
// there is none.
func lastBreak(r []rune) int {
	for i := len(r) - 1; i > 0; i-- {
		switch r[i] {
		case '\n', ' ', '\t':
			return i
		}
	}
	return 0
}

// sessions maps a Telegram chat to an ADK session id, so a conversation
// survives between messages. The mutex is there because the poll loop is the
// only writer today, and a second one would otherwise be silent corruption.
type sessions struct {
	mu  sync.Mutex
	gen map[int64]int
}

func newSessions() *sessions {
	return &sessions{gen: map[int64]int{}}
}

// next returns the session id for a chat. With reset it advances a per-chat
// generation counter, so /new lands the next turn in a fresh ADK session and
// the previous history is left untouched on the server.
func (s *sessions) next(chatID int64, reset bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if reset {
		s.gen[chatID]++
	}
	if gen := s.gen[chatID]; gen > 0 {
		return fmt.Sprintf("tg-%d-%d", chatID, gen)
	}
	return "tg-" + strconv.FormatInt(chatID, 10)
}

// --- the bot's behaviour -----------------------------------------------------

// botRunner is what handleMessage needs. An interface, so a test can drive the
// message handling without a model or a network.
type botRunner interface {
	Turn(ctx context.Context, userID, sessionID, input string) (labrun.Result, error)
}

type handler struct {
	cfg      config
	bot      *bot
	runner   botRunner
	sessions *sessions
	// retryBackoff is how long the poll loop waits after a failed poll. Zero
	// means backoffOnError; a test shortens it so the retry path is reachable
	// in milliseconds rather than seconds.
	retryBackoff time.Duration
}

// backoff is the pause before retrying a failed poll.
func (h *handler) backoff() time.Duration {
	if h.retryBackoff > 0 {
		return h.retryBackoff
	}
	return backoffOnError
}

func (h *handler) allowed(userID int64) bool {
	return len(h.cfg.allowed) == 0 || h.cfg.allowed[userID]
}

// welcome is also the answer to a non-allowed user, minus the greeting: it
// tells the owner which id to put in TELEGRAM_ALLOW_USER_IDS, which is the one
// piece of information they cannot get from the bot itself.
func (h *handler) welcome(userID int64) string {
	name := h.cfg.userName
	if name == "" {
		name = "there"
	}
	return fmt.Sprintf(`Hi %s. I am an ADK agent on Telegram.

/help — what I can do
/new  — start a fresh conversation
/id   — show your Telegram user id

Your id is %d.`, name, userID)
}

const helpText = `I am a Google ADK agent behind a Telegram bot.
Send me any text and I answer it. I can also tell you the current date and time
in a timezone you name.
/new starts a fresh conversation; the older one is kept on the server.
/id shows your numeric Telegram user id.`

// handle processes one update. It never returns an error: a bad message must
// not stop the poll loop, so everything is logged and the loop continues.
func (h *handler) handle(ctx context.Context, u update) {
	msg := u.Message
	if msg == nil || msg.From == nil {
		return // channel posts and edits carry no user to authorise
	}
	userID := msg.From.ID
	chatID := msg.Chat.ID

	if !h.allowed(userID) {
		log.Printf("refusing user id=%d (%s) in chat %d: not in TELEGRAM_ALLOW_USER_IDS",
			userID, msg.From.Username, chatID)
		h.reply(ctx, chatID, fmt.Sprintf(
			"Sorry, this bot is private. Its owner can add your id to TELEGRAM_ALLOW_USER_IDS:\n%d", userID))
		return
	}
	if msg.From.IsBot {
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		h.reply(ctx, chatID, "I only handle text messages, please send the request as text.")
		return
	}

	if cmd, ok := command(text); ok {
		switch cmd {
		case "start":
			h.reply(ctx, chatID, h.welcome(userID))
		case "help":
			h.reply(ctx, chatID, helpText)
		case "id":
			h.reply(ctx, chatID, fmt.Sprintf("Your Telegram user id is %d.", userID))
		case "new":
			h.sessions.next(chatID, true)
			h.reply(ctx, chatID, "Started a fresh conversation.")
		default:
			h.reply(ctx, chatID, "Unknown command. Try /help.")
		}
		return
	}

	h.typing(ctx, chatID)
	h.turn(ctx, chatID, userID, text)
}

// turn runs one agent turn and posts its answer.
func (h *handler) turn(ctx context.Context, chatID, userID int64, text string) {
	sessionID := h.sessions.next(chatID, false)
	res, err := h.runner.Turn(ctx, strconv.FormatInt(chatID, 10), sessionID, text)
	if err != nil {
		log.Printf("chat %d session %s: %v", chatID, sessionID, err)
		h.reply(ctx, chatID, "The agent failed on that request. Check the bot's log for details.")
		return
	}
	answer := strings.TrimSpace(res.Final)
	if answer == "" {
		answer = "The agent returned an empty answer."
	}
	if len(res.ToolCalls) > 0 {
		log.Printf("chat %d session %s: tools=%v", chatID, sessionID, res.ToolCalls)
	}
	h.reply(ctx, chatID, answer)
}

func (h *handler) reply(ctx context.Context, chatID int64, text string) {
	if err := h.bot.send(ctx, chatID, text); err != nil {
		log.Printf("send to chat %d: %v", chatID, err)
	}
}

func (h *handler) typing(ctx context.Context, chatID int64) {
	h.bot.typing(ctx, chatID)
}

// command recognises a /command, tolerating the "@BotName" suffix Telegram adds
// in group chats.
func command(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	word := strings.Fields(text)[0]
	word = strings.TrimPrefix(word, "/")
	if at := strings.Index(word, "@"); at >= 0 {
		word = word[:at]
	}
	return strings.ToLower(word), true
}

// --- wiring ------------------------------------------------------------------

// run loads apps/.env, resolves the provider, builds the agent and polls.
func run(ctx context.Context, opts options) error {
	// apps/.env is found by walking up from the working directory, so the same
	// code works from this folder and from the repository root.
	if err := modelcfg.LoadEnv("."); err != nil {
		return fmt.Errorf("load apps/.env: %w", err)
	}
	cfg, err := loadConfig(opts.allowAll)
	if err != nil {
		return err
	}

	m, choice, err := modelcfg.Load(ctx)
	if err != nil {
		return err
	}
	log.Printf("model: %s", choice.Reason)

	a, err := newAgent(m)
	if err != nil {
		return err
	}
	h := &handler{
		cfg:      cfg,
		bot:      newBot(cfg.token, opts.apiBase),
		runner:   labrun.NewRunner(a),
		sessions: newSessions(),
	}

	if opts.dryRun {
		log.Printf("dry run: config and agent are fine; not polling Telegram")
		log.Printf("  allow: %s", allowSummary(cfg.allowed))
		return nil
	}
	if cfg.token == "" {
		return errors.New("TELEGRAM_BOT_API is empty in apps/.env: get a token from @BotFather (https://t.me/BotFather)")
	}

	me, err := h.bot.getMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram getMe (is TELEGRAM_BOT_API correct?): %w", err)
	}
	banner(me, cfg, choice)

	return poll(ctx, h)
}

// poll is the long-poll loop. A failed poll is retried after a pause: Telegram
// is remote and eventually reachable again, so exiting on the first network
// blip would make the bot useless on a laptop that sleeps.
func poll(ctx context.Context, h *handler) error {
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return err // Ctrl-C: an expected, quiet exit
		}

		updates, err := h.bot.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var rl *rateLimitError
			if errors.As(err, &rl) {
				log.Printf("getUpdates: %v", err)
				sleep(ctx, rl.retryAfter)
				continue
			}
			log.Printf("getUpdates: %v", err)
			sleep(ctx, h.backoff())
			continue
		}

		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			h.handle(ctx, u)
		}
	}
}

// sleep waits for d, or returns early when the context is cancelled.
func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func allowSummary(allowed map[int64]bool) string {
	if len(allowed) == 0 {
		return "EVERY user (-allow-all)"
	}
	ids := make([]string, 0, len(allowed))
	for id := range allowed {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	return strings.Join(ids, ", ")
}

func banner(me *user, cfg config, choice modelcfg.Choice) {
	handle := "(no username)"
	if me.Username != "" {
		handle = "@" + me.Username
	}
	log.Printf("telegram bot connected")
	log.Printf("  bot:    %s (%s) id=%d", handle, me.FirstName, me.ID)
	if me.Username != "" {
		log.Printf("  link:   https://t.me/%s", me.Username)
	}
	log.Printf("  model:  %s", choice.Reason)
	if cfg.userName != "" {
		log.Printf("  name:   %s", cfg.userName)
	}
	log.Printf("  allow:  %s", allowSummary(cfg.allowed))
	log.Printf("  help:   /help, /new, /id — Ctrl-C to stop")
	log.Printf("long-polling for updates…")
}

func main() {
	dryRun := flag.Bool("dry-run", false, "resolve the config and build the agent, then exit without polling Telegram")
	allowAll := flag.Bool("allow-all", false, "serve every Telegram user (an empty TELEGRAM_ALLOW_USER_IDS is a refusal to start)")
	apiBase := flag.String("api-base", "", "override the Telegram API base URL (used by tests)")
	flag.Parse()

	log.SetFlags(log.LstdFlags)

	// Ctrl-C and SIGTERM both cancel the poll loop, so a session is not left
	// with a half-written turn.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := options{dryRun: *dryRun, allowAll: *allowAll, apiBase: *apiBase}
	if err := run(ctx, opts); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("telegram bot: %v", err)
	}
}
