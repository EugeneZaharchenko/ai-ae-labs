// Package modelcfg decides which model backend a lab runs and builds it.
//
// It exists because that decision is the same decision in every lab, and it is
// not a trivial one: credentials have a priority order, a model name may carry
// its own route prefix, a raised local gateway must see all traffic, and each
// wrong answer has to fail loudly with a name the reader can act on. Labs that
// answered it themselves answered it differently — some hardcoded a direct
// provider client, which meant apps/.env was never read, MODEL and
// DEFAULT_MODEL_PROVIDER did nothing, and a raised gateway was silently
// bypassed, leaving holes in the traces and the cost ledger.
//
// Week 1's labs keep their own provider.go on purpose: there, model choice is
// the subject being taught, so the table and its reasoning belong in the
// learner's hands. This package is for the days whose subject is something else
// and which only need the capability.
//
// What lives here:
//
//   - the provider → default-model table (Defaults);
//   - resolving a choice from the environment (Choose) and building it (Load);
//   - instantiating the backend through pimodels (Build).
//
// What does not: reading apps/.env. That is application bootstrap, not a
// property of a provider, so callers do it explicitly with LoadEnv before Load.
//
// Verified against google.golang.org/adk/v2 v2.4.0 and pi-go v0.2.3 (09/2026).
package modelcfg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dimetron/pi-go/pimodels"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/adkenv"
)

// Default is one provider's default model.
//
// A struct rather than generics: the model type is always string, so a type
// parameter would be a parameter that never varies — more machinery than the
// problem has (AGENTS.md §4, "clear is better than clever").
type Default struct {
	// Provider is the value of DEFAULT_MODEL_PROVIDER and the route prefix
	// for agentgateway.
	Provider string
	// Model is the default model. For agentgateway/ollama it is a local tag,
	// because that route reaches a local daemon the gateway uses as its
	// Ollama backend.
	Model string
	// EnvVar is the variable whose presence enables the provider ("" means the
	// provider needs no credential).
	EnvVar string
	// EnvModel overrides the model for this provider alone.
	EnvModel string
}

// defaults is the provider → default-model table.
//
// The three agentgateway rows are three DIFFERENT routes of one gateway, which
// is why they are separate: `agentgateway/ollama` reaches a local daemon,
// `-cloud` tags reach api.ollama.com, and `agentgateway/openai` and
// `agentgateway/gemini` reach vendor APIs. To the agent these are just model
// names; the difference in behaviour — price, licence, data residency — comes
// from the route, not from agent code.
//
// Order matters: agentgateway first. When the local gateway is up, all traffic
// should go through it, otherwise traces and cost accounting have holes and stop
// being evidence. This order is both the auto-detection priority in choose and
// the order in AGENTS.md §3.
//
// Models are deliberately "as of 08/2026" (AGENTS.md §3). An id a provider no
// longer serves fails loudly with that provider's own error rather than silently
// benchmarking under a stale name. There is no floating tag for
// agentgateway/ollama — local tags are concrete — so the size that fits a laptop
// is named.
var defaults = []Default{
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

// geminiAPIKeyAliases are the names a Gemini key is accepted under.
//
// adkenv.Load only fills variables the environment does not already have, so a
// caller that exported GEMINI_API_KEY — the name used by .env-example and by
// pimodels.APIKeyEnvVar — would otherwise be told no provider is configured
// while holding a working key.
var geminiAPIKeyAliases = []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}

// Choice is what was decided, and why.
//
// The reason is returned to the caller on purpose: whoever sees a wrong answer
// should also see which provider produced it.
type Choice struct {
	Provider string
	Model    string
	Reason   string
}

// Defaults returns the provider table.
//
// A copy, so a caller cannot reorder or mutate the policy for everyone else.
func Defaults() []Default {
	out := make([]Default, len(defaults))
	copy(out, defaults)
	return out
}

// LoadEnv loads the repository's apps/.env, searching upward from dir, and
// tolerates its absence — the offline path must work with no keys at all.
//
// An explicit export always wins over the file: adkenv.Load only fills what is
// not already set, so CI and one-off runs with a different key need no edit.
//
// Call this BEFORE Load. Provider auto-detection reads the process environment,
// so keys that are still in the file are invisible to it.
func LoadEnv(dir string) error {
	if err := adkenv.Load(dir); err != nil && !errors.Is(err, adkenv.ErrNotFound) {
		return err
	}
	return nil
}

// Load resolves the provider and model from MODEL and DEFAULT_MODEL_PROVIDER,
// builds the backend, and returns the choice alongside the model.
//
// The build error is enriched with the variable the provider wanted: without
// that, a caller sees "API key not valid" and cannot tell which key was sought.
// This is the one place where a pimodels error meets the name of a variable, so
// the hint lives here rather than in each caller.
//
// Precondition: LoadEnv has run — otherwise keys from apps/.env are not in the
// environment and auto-detection will not see them.
func Load(ctx context.Context) (model.LLM, Choice, error) {
	choice, err := Choose(env("MODEL"), env("DEFAULT_MODEL_PROVIDER"))
	if err != nil {
		return nil, Choice{}, err
	}

	m, err := Build(ctx, choice.Provider, choice.Model)
	if err != nil {
		return nil, choice, fmt.Errorf(
			"не вдалося створити модель %q (провайдер %s): %w\n"+
				"Перевірте %s в apps/.env або в оточенні",
			choice.Model, choice.Provider, err, credentialVarName(choice.Provider))
	}
	return m, choice, nil
}

// credentialVarName names the variable a caller should set for a provider.
//
// The table is asked first, and that order matters. pimodels.APIKeyEnvVar
// derives the name from the provider string, which produces a plausible-looking
// but non-existent variable for a route: for "agentgateway/gemini" it returns
// "AGENTGATEWAY/GEMINI_API_KEY", complete with a slash. A reader sent looking
// for that name finds nothing and learns nothing. The table already records the
// variable that actually enables each provider, so it is the better source; the
// derived name remains the fallback for a provider this table does not list.
func credentialVarName(provider string) string {
	if d, ok := resolveProvider(provider); ok && d.EnvVar != "" {
		return d.EnvVar
	}
	return pimodels.APIKeyEnvVar(provider)
}

// Choose decides which provider and model to run.
//
// Resolution order:
//
//  1. DEFAULT_MODEL_PROVIDER is set — the provider is named; the model comes
//     from <PROVIDER>_MODEL, otherwise the provider's default.
//  2. MODEL is set (and the provider is not) — the provider comes from the
//     model name's route prefix, or from pimodels when there is none.
//  3. Neither is set — the provider is whichever credential is present, and
//     the model is its default.
//
// Every refusal is loud and names the known providers, so the reader does not
// have to go looking for the table.
//
// There is deliberately no silent fallback to gemini. A caller with no key used
// to be told "gemini → gemini-3.8-flash" and then fail at the first request with
// another provider's error, which hides the cause. A provider nobody chose is a
// guess, not a default, and a guess has to be visible.
func Choose(modelEnv, providerEnv string) (Choice, error) {
	if provider := strings.TrimSpace(providerEnv); provider != "" {
		d, ok := resolveProvider(provider)
		if !ok {
			return Choice{}, fmt.Errorf("невідомий провайдер %q у DEFAULT_MODEL_PROVIDER; відомі: %s",
				provider, knownProviders())
		}
		return choiceFor(d, modelEnv), nil
	}

	if model := strings.TrimSpace(modelEnv); model != "" {
		fromPrefix, ok := providerForModel(model)
		if !ok {
			return Choice{}, fmt.Errorf(
				"не вдалося визначити провайдера для MODEL=%q: ні префікс маршруту, ні pimodels його не знають.\n"+
					"Допишіть маршрут у MODEL (напр. MODEL=ollama/%s) або задайте DEFAULT_MODEL_PROVIDER.\n"+
					"Відомі провайдери: %s",
				model, model, knownProviders())
		}
		return choiceFor(fromPrefix, model), nil
	}

	d, ok := autoProvider()
	if !ok {
		return Choice{}, fmt.Errorf(
			"жодного провайдера не налаштовано: немає ні ключа, ні MODEL, ні DEFAULT_MODEL_PROVIDER.\n"+
				"Впишіть ключ у apps/.env (шаблон — apps/.env-example) або задайте DEFAULT_MODEL_PROVIDER.\n"+
				"Відомі провайдери: %s",
			knownProviders())
	}
	return choiceFor(d, ""), nil
}

// Build instantiates an ADK model through github.com/dimetron/pi-go/pimodels,
// which resolves Gemini, OpenAI, Ollama and gateway routes and handles keys and
// base URLs.
//
// Two cases must be told apart:
//
//   - `agentgateway/<vendor>/<model>` is a gateway route, not a vendor API. The
//     endpoint is the gateway: AGENTGATEWAY_BASE_URL when set, otherwise the
//     pimodels default (http://localhost:4000). OLLAMA_BASE_URL is NOT passed
//     here — it names a different service and would override the gateway's
//     address and break routing inside it.
//   - any other model plus OLLAMA_BASE_URL names a specific endpoint, and is
//     passed explicitly.
//
// The `ollama/` retry is conditional, and the condition is the point. It exists
// for a bare local tag (`qwen3.5:4b-mlx`) that pimodels cannot route at all.
// Unconditional, it acted as a generator of false successes: `ollama/` needs no
// key and calls nothing, so the model always built and the real cause — "api key
// is required" — disappeared, surfacing later as an unrelated 404. The Resolve
// check restores exactly what the retry was written for.
func Build(ctx context.Context, provider, modelName string) (model.LLM, error) {
	var opts []pimodels.Option
	if isAgentGateway(provider) {
		// The gateway address has to be passed explicitly: pimodels does not
		// read this variable as an endpoint, so without it a gateway on a
		// non-default port was silently ignored and the client hit :4000.
		// adkenv.Key, so an empty-but-set variable cannot erase the pimodels
		// default.
		if base, ok := adkenv.Key("AGENTGATEWAY_BASE_URL"); ok {
			opts = append(opts, pimodels.WithBaseURL(base))
		}
	} else if base, ok := adkenv.Key("OLLAMA_BASE_URL"); ok {
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

// resolveModelName returns the model name from env, or the fallback.
//
// Separated so it can be tested: the "run 2-3 models by changing one variable"
// exercise rests on this invariant. A silently ignored MODEL would compare a
// model against itself and print a wholly plausible table — the worst kind of
// error, because nothing fails.
func resolveModelName(env, fallback string) string {
	if strings.TrimSpace(env) == "" {
		return fallback
	}
	return strings.TrimSpace(env)
}

// resolveProvider returns the table row for a provider (case-insensitive) and
// reports whether it is known at all.
func resolveProvider(name string) (Default, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, d := range defaults {
		if d.Provider == want {
			return d, true
		}
	}
	return Default{}, false
}

// providerForModel guesses the table row from a model name's prefix.
//
// For "MODEL set, DEFAULT_MODEL_PROVIDER not": the prefix already carries the
// answer, and making the caller repeat it in a second variable guarantees the
// two will disagree.
//
// Three passes, most specific first:
//
//  1. Longest full `provider + "/"` match, so `agentgateway/gemini/...` does not
//     match `agentgateway/ollama`. Without this pass a Gemini request would go
//     to the local Ollama daemon and fail with "model not found" — a message
//     that sends the reader hunting for a missing `ollama pull` instead of
//     pointing at the routing bug.
//  2. First-segment match, for routes inside the gateway that have no row of
//     their own: `agentgateway/deepseek-...` is the same gateway even though the
//     table has no such line. The model still comes from MODEL, so the row only
//     fixes the route family.
//  3. A bare vendor id (`gpt-5.6-luna`, `deepseek-v4.1-flash:cloud`) with no
//     prefix. Here pimodels is asked — it is the source of truth for name →
//     provider. Without this pass such a model would be labelled gemini, and
//     with OLLAMA_BASE_URL set we would attach an Ollama endpoint to an OpenAI
//     model: an error that looks like a provider problem.
func providerForModel(modelName string) (Default, bool) {
	lower := strings.ToLower(strings.TrimSpace(modelName))

	var best Default
	var found bool
	for _, d := range defaults {
		if strings.HasPrefix(lower, d.Provider+"/") && (!found || len(d.Provider) > len(best.Provider)) {
			best, found = d, true
		}
	}
	if found {
		return best, true
	}

	segment, _, _ := strings.Cut(lower, "/")
	for _, d := range defaults {
		if top, _, ok := strings.Cut(d.Provider, "/"); ok && top == segment {
			return d, true
		}
	}

	// Bare id: only pimodels knows the provider.
	if info, err := pimodels.Resolve(modelName); err == nil {
		if d, ok := resolveProvider(info.Provider); ok {
			return d, true
		}
	}
	return Default{}, false
}

// autoProvider picks a provider by the credentials present in the environment.
//
// The order is the defaults order, i.e. the AGENTS.md §3 priority:
// agentgateway → ollama → gemini → openai.
func autoProvider() (Default, bool) {
	for _, d := range defaults {
		if _, ok := providerCredential(d); ok {
			return d, true
		}
	}
	return Default{}, false
}

// providerCredential returns a provider's credential — key or base URL — and
// whether it is set at all.
//
// Read through adkenv.Key, not os.Getenv: an empty-but-set variable is a common
// way to break CI, and treating it as "provider configured" yields an opaque 401
// instead of an honest "not configured".
func providerCredential(d Default) (string, bool) {
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

// envVarAliases expands a provider's variable name to all of its synonyms.
//
// The name in the table and the name pimodels actually reads can differ
// (GOOGLE_API_KEY vs GEMINI_API_KEY), so Gemini checks both.
func envVarAliases(name string) []string {
	if name == "GOOGLE_API_KEY" {
		return geminiAPIKeyAliases
	}
	return []string{name}
}

// choiceFor fills in the choice.
//
// Model precedence: the argument first (MODEL or an explicit value), then the
// provider's own variable (<PROVIDER>_MODEL), then the table default. The
// provider variable outranks the default because it is more specific: a caller
// who set OLLAMA_MODEL named the model more precisely than the table did.
func choiceFor(d Default, modelEnv string) Choice {
	model := strings.TrimSpace(modelEnv)
	if model == "" && d.EnvModel != "" {
		if v, ok := adkenv.Key(d.EnvModel); ok {
			model = v
		}
	}
	model = qualifyGatewayModel(d.Provider, resolveModelName(model, d.Model))
	return Choice{
		Provider: d.Provider,
		Model:    model,
		Reason:   fmt.Sprintf("%s → %s", d.Provider, model),
	}
}

// qualifyGatewayModel appends the gateway route to a bare model name when the
// provider is agentgateway.
//
// This is routing, not cosmetics: pimodels decides "gateway or vendor" SOLELY
// from the name's prefix. A bare `gemini-3.8-flash` under provider
// `agentgateway/gemini` went to pimodels unchanged and resolved to a DIRECT
// Gemini client to Google, silently bypassing the gateway. From outside it
// looked like "grounding does not work through the gateway" even though the
// gateway was never touched; traces and cost accounting had the same holes.
//
// The rules, most specific match first:
//
//   - the name already carries `agentgateway/` — leave it (MODEL given whole);
//   - the name already carries the vendor route segment (`gemini/...`) — adding
//     `agentgateway/` is enough, otherwise the segment would double;
//   - a bare name — append the whole route (`agentgateway/gemini/`).
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

// knownProviders lists the providers for an error message.
func knownProviders() string {
	names := make([]string, 0, len(defaults))
	for _, d := range defaults {
		names = append(names, d.Provider)
	}
	return strings.Join(names, ", ")
}

// isAgentGateway reports whether a provider is a local gateway route.
func isAgentGateway(provider string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(provider)), "agentgateway")
}

// env reads a variable, treating only a non-empty value as set.
func env(name string) string {
	v, _ := adkenv.Key(name)
	return v
}
