package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dimetron/pi-go/pimodels"
)

// credentialEnvVars — усе, що вмикає провайдера або змінює вибір моделі.
//
// Тести вибору мусять починати з чистого оточення: якщо в машині розробника
// експортовано GOOGLE_API_KEY, а в CI — ні, той самий тест перевіряв би дві
// різні програми. t.Setenv(..., "") виставляє змінну порожньою, а adkenv.Key
// трактує порожню як незадану — саме те, що потрібно.
var credentialEnvVars = []string{
	"MODEL", "DEFAULT_MODEL_PROVIDER",
	"AGENTGATEWAY_BASE_URL", "AGENTGATEWAY_MODEL", "AGENTGATEWAY_API_KEY",
	"OLLAMA_BASE_URL", "OLLAMA_MODEL", "OLLAMA_API_KEY",
	"GOOGLE_API_KEY", "GEMINI_API_KEY", "GEMINI_MODEL",
	"OPENAI_API_KEY", "OPENAI_MODEL",
}

// clearCredentials обнуляє всі змінні вибору моделі на час тесту.
func clearCredentials(t *testing.T) {
	t.Helper()
	for _, name := range credentialEnvVars {
		t.Setenv(name, "")
	}
}

// TestResolveModelName pins the one invariant the whole Lab 1 benchmark rests
// on: the model name comes from the MODEL environment variable, and falls back
// to the provider's default when it is absent.
//
// Why this is worth a test at all: Завдання 5 asks the learner to run the same
// prompt on two models by changing ONE variable. If MODEL were ignored, the
// benchmark would silently compare a model against itself and produce a
// perfectly plausible table — the worst kind of wrong, because nothing fails.
func TestResolveModelName(t *testing.T) {
	const fallback = "gemini-3.8-flash"

	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "empty falls back to the provider default", env: "", want: fallback},
		{name: "whitespace is not treated as a value", env: "   ", want: fallback},
		{name: "explicit model wins", env: "gpt-5.6-luna", want: "gpt-5.6-luna"},
		{name: "a local Ollama tag passes through", env: "qwen3.5:4b-mlx", want: "qwen3.5:4b-mlx"},
		{name: "an agentgateway route passes through whole", env: "agentgateway/openai/gpt-5.6-luna", want: "agentgateway/openai/gpt-5.6-luna"},
		{name: "surrounding whitespace is trimmed", env: "  gemini-3.8-flash  ", want: "gemini-3.8-flash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveModelName(tt.env, fallback); got != tt.want {
				t.Errorf("resolveModelName(%q, %q) = %q, want %q", tt.env, fallback, got, tt.want)
			}
		})
	}
}

// TestResolveProvider pins that each provider name in the table is reachable by
// name, case-insensitively, and that an unknown name is reported as unknown
// rather than silently falling back to something plausible.
func TestResolveProvider(t *testing.T) {
	tests := []struct {
		name string
		want string
		ok   bool
	}{
		{name: "agentgateway/ollama", want: "qwen3.5:4b-mlx", ok: true},
		{name: "agentgateway/openai", want: "gpt-5.6-luna", ok: true},
		{name: "agentgateway/gemini", want: "gemini-3.8-flash", ok: true},
		{name: "AgentGateway/Ollama", want: "qwen3.5:4b-mlx", ok: true}, // case-insensitive
		{name: "  gemini  ", want: "gemini-3.8-flash", ok: true},        // trimmed
		{name: "ollama", want: "deepseek-v4.1-flash:cloud", ok: true},
		{name: "openai", want: "gpt-5.6-luna", ok: true},
		{name: "gemini", want: "gemini-3.8-flash", ok: true},
		{name: "anthropic", ok: false},
		{name: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveProvider(tt.name)
			if ok != tt.ok {
				t.Fatalf("resolveProvider(%q) ok = %v, want %v", tt.name, ok, tt.ok)
			}
			if !ok {
				return
			}
			if got.Model != tt.want {
				t.Errorf("resolveProvider(%q).Model = %q, want %q", tt.name, got.Model, tt.want)
			}
		})
	}
}

// TestProviderForModel pins the longest-prefix rule.
//
// The trap it guards: `agentgateway/gemini/...` must not match
// `agentgateway/ollama` just because both start with `agentgateway/`. A wrong
// match here sends a Gemini request to the local Ollama daemon — which fails
// with "model not found", a message that sends the learner hunting for a
// missing `ollama pull` instead of a routing bug.
func TestProviderForModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
		ok    bool
	}{
		{model: "agentgateway/openai/gpt-5.6-luna", want: "agentgateway/openai", ok: true},
		{model: "agentgateway/gemini/gemini-3.8-flash", want: "agentgateway/gemini", ok: true},
		{model: "agentgateway/ollama/qwen3.5:4b-mlx", want: "agentgateway/ollama", ok: true},
		{model: "agentgateway/deepseek-v4.1-flash:cloud", want: "agentgateway/ollama", ok: true},
		{model: "ollama/qwen3.5:4b-mlx", want: "ollama", ok: true},
		// Bare vendor ids: pimodels is authoritative for these, so they resolve
		// to their real provider instead of defaulting to gemini.
		{model: "gpt-5.6-luna", want: "openai", ok: true},
		{model: "deepseek-v4.1-flash:cloud", want: "ollama", ok: true},
		{model: "gemini-3.8-flash", want: "gemini", ok: true},
		{model: "not-a-real-model-xyz", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, ok := providerForModel(tt.model)
			if ok != tt.ok {
				t.Fatalf("providerForModel(%q) ok = %v, want %v", tt.model, ok, tt.ok)
			}
			if ok && got.Provider != tt.want {
				t.Errorf("providerForModel(%q).Provider = %q, want %q", tt.model, got.Provider, tt.want)
			}
		})
	}
}

// TestChooseModelPrecedence pins the resolution order documented on chooseModel.
//
// Each case also asserts the provider, not just the model: the failure this
// guards against is a correct-looking model name routed to the wrong backend,
// which answers — or fails — for reasons that have nothing to do with the model
// the learner thinks they are testing.
//
// Three cases assert an error rather than a choice. They are the same bug at
// three depths: a provider nobody selected. Substituting gemini for it (what
// chooseModel used to do) turns "you have not configured anything" into a
// plausible-looking pair of names in the log, and the mistake only surfaces at
// the first request — as another provider's error.
func TestChooseModelPrecedence(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantProvider string
		wantModel    string
		wantErr      bool
		wantErrText  string
	}{
		{
			name:        "nothing set is an error, not a guess at gemini",
			env:         nil,
			wantErr:     true,
			wantErrText: "жодного провайдера не налаштовано",
		},
		{
			name:         "explicit provider wins",
			env:          map[string]string{"DEFAULT_MODEL_PROVIDER": "openai"},
			wantProvider: "openai",
			wantModel:    "gpt-5.6-luna",
		},
		{
			name:         "explicit agentgateway route wins",
			env:          map[string]string{"DEFAULT_MODEL_PROVIDER": "agentgateway/gemini"},
			wantProvider: "agentgateway/gemini",
			// Ім'я мусить вийти кваліфікованим: голе воно резолвиться в
			// прямий Gemini-клієнт до Google, тихо обминаючи шлюз.
			wantModel: "agentgateway/gemini/gemini-3.8-flash",
		},
		{
			name:         "MODEL alone infers the provider from its prefix",
			env:          map[string]string{"MODEL": "agentgateway/openai/gpt-5.6-luna"},
			wantProvider: "agentgateway/openai",
			wantModel:    "agentgateway/openai/gpt-5.6-luna",
		},
		{
			name:         "MODEL alone with a bare id takes the provider from pimodels",
			env:          map[string]string{"MODEL": "gpt-5.6-luna"},
			wantProvider: "openai",
			wantModel:    "gpt-5.6-luna",
		},
		{
			name:         "MODEL wins over the provider default",
			env:          map[string]string{"DEFAULT_MODEL_PROVIDER": "openai", "MODEL": "gpt-5.6-luna"},
			wantProvider: "openai",
			wantModel:    "gpt-5.6-luna",
		},
		{
			name:         "the provider model variable supplies the model",
			env:          map[string]string{"DEFAULT_MODEL_PROVIDER": "ollama", "OLLAMA_MODEL": "qwen3.5:4b-mlx"},
			wantProvider: "ollama",
			wantModel:    "qwen3.5:4b-mlx",
		},
		{
			name:         "an agentgateway base URL turns the gateway on by itself",
			env:          map[string]string{"AGENTGATEWAY_BASE_URL": "http://localhost:4000"},
			wantProvider: "agentgateway/ollama",
			wantModel:    "agentgateway/ollama/qwen3.5:4b-mlx",
		},
		{
			name:         "a Gemini key turns on the gemini provider",
			env:          map[string]string{"GOOGLE_API_KEY": "key"},
			wantProvider: "gemini",
			wantModel:    "gemini-3.8-flash",
		},
		{
			name:         "GEMINI_API_KEY is accepted as an alias",
			env:          map[string]string{"GEMINI_API_KEY": "key"},
			wantProvider: "gemini",
			wantModel:    "gemini-3.8-flash",
		},
		{
			name:         "an OpenAI key turns on the openai provider",
			env:          map[string]string{"OPENAI_API_KEY": "key"},
			wantProvider: "openai",
			wantModel:    "gpt-5.6-luna",
		},
		{
			name: "an unknown provider is a loud error, not a silent fallback",
			env:  map[string]string{"DEFAULT_MODEL_PROVIDER": "anthropic"},
			// anthropic має зміни, але ADK Go v2.4.0 не має бекенда Anthropic,
			// тож краще сказати це на старті, ніж падати пізніше.
			wantErr:     true,
			wantErrText: "невідомий провайдер",
		},
		{
			name:        "MODEL that nothing can route is an error, not a gemini guess",
			env:         map[string]string{"MODEL": "not-a-real-model-xyz"},
			wantErr:     true,
			wantErrText: "не вдалося визначити провайдера",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCredentials(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := chooseModel(os.Getenv("MODEL"), os.Getenv("DEFAULT_MODEL_PROVIDER"))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("chooseModel() = %+v, want an error", got)
				}
				if !strings.Contains(err.Error(), tt.wantErrText) {
					t.Errorf("chooseModel() error = %q, want it to contain %q", err, tt.wantErrText)
				}
				// The error is the whole remedy: it has to name the providers
				// that would have worked, or it just moves the search.
				if !strings.Contains(err.Error(), "agentgateway/ollama") {
					t.Errorf("chooseModel() error = %q, want it to list the known providers", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("chooseModel() unexpected error: %v", err)
			}
			if got.Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", got.Provider, tt.wantProvider)
			}
			if got.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", got.Model, tt.wantModel)
			}
		})
	}
}

// TestQualifyGatewayModel pins the routing rule the grounding debug case
// exposed: pimodels chooses "gateway or vendor" by the NAME PREFIX alone, so a
// bare model name under an agentgateway provider must come out qualified —
// otherwise the request silently bypasses the gateway and goes to the vendor.
func TestQualifyGatewayModel(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     string
	}{
		{
			name:     "bare model gets the full gateway route",
			provider: "agentgateway/gemini",
			model:    "gemini-3.8-flash",
			want:     "agentgateway/gemini/gemini-3.8-flash",
		},
		{
			name:     "vendor-prefixed model only gets the gateway segment",
			provider: "agentgateway/gemini",
			model:    "gemini/gemini-3.8-flash",
			want:     "agentgateway/gemini/gemini-3.8-flash",
		},
		{
			name:     "already-qualified name is untouched",
			provider: "agentgateway/gemini",
			model:    "agentgateway/gemini/gemini-3.8-flash",
			want:     "agentgateway/gemini/gemini-3.8-flash",
		},
		{
			name:     "bare ollama tag gets the gateway route too",
			provider: "agentgateway/ollama",
			model:    "qwen3.5:4b-mlx",
			want:     "agentgateway/ollama/qwen3.5:4b-mlx",
		},
		{
			name:     "non-gateway provider is untouched",
			provider: "gemini",
			model:    "gemini-3.8-flash",
			want:     "gemini-3.8-flash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := qualifyGatewayModel(tt.provider, tt.model); got != tt.want {
				t.Errorf("qualifyGatewayModel(%q, %q) = %q, want %q",
					tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

// TestAgentGatewayRoutesResolveThroughPimodels pins that every table row is a
// name pimodels itself recognises, and pins HOW it resolves it.
//
// The contract, verified against pi-go v0.2.3: an `agentgateway/...` name
// resolves to provider "agentgateway" with the rest of the name — including any
// vendor segment — left intact as the model. The gateway does the routing; the
// client must forward the layered name rather than strip it. Getting this wrong
// by "helpfully" trimming `openai/` would send a bare `gpt-5.6-luna` to a
// gateway whose route table keys on the prefix.
//
// This is the cheap check that catches the expensive mistake: a typo in the
// table would otherwise surface only at runtime, as an error from a gateway the
// learner has not started yet — indistinguishable from "the gateway is down".
func TestAgentGatewayRoutesResolveThroughPimodels(t *testing.T) {
	for _, d := range providerDefaults {
		if !isAgentGateway(d.Provider) {
			continue
		}
		t.Run(d.Provider, func(t *testing.T) {
			name := d.Provider + "/" + d.Model
			info, err := pimodels.Resolve(name)
			if err != nil {
				t.Fatalf("pimodels.Resolve(%q): %v", name, err)
			}
			top, _, _ := strings.Cut(d.Provider, "/")
			if info.Provider != top {
				t.Errorf("pimodels.Resolve(%q).Provider = %q, want %q", name, info.Provider, top)
			}
			if !strings.Contains(info.Model, d.Model) {
				t.Errorf("pimodels.Resolve(%q).Model = %q, want it to carry %q", name, info.Model, d.Model)
			}
		})
	}
}

// TestProviderModelPairsResolve pins that each non-gateway row resolves to the
// provider it claims.
//
// The `ollama` row is the one that needs the prefix: a bare
// `deepseek-v4.1-flash:cloud` resolves to ollama anyway via its `-cloud` tag,
// but a bare local tag like `qwen3.5:4b-mlx` resolves to nothing at all.
// Prefixing keeps both forms working, which is why createModel retries with the
// prefix.
func TestProviderModelPairsResolve(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     string
	}{
		{provider: "gemini", model: "gemini-3.8-flash", want: "gemini"},
		{provider: "openai", model: "gpt-5.6-luna", want: "openai"},
		{provider: "ollama", model: "deepseek-v4.1-flash:cloud", want: "ollama"},
		{provider: "ollama", model: "qwen3.5:4b-mlx", want: "ollama"}, // needs the prefix
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			name := tt.model
			if tt.provider == "ollama" {
				name = "ollama/" + tt.model
			}
			info, err := pimodels.Resolve(name)
			if err != nil {
				t.Fatalf("pimodels.Resolve(%q): %v", name, err)
			}
			if info.Provider != tt.want {
				t.Errorf("pimodels.Resolve(%q).Provider = %q, want %q", name, info.Provider, tt.want)
			}
		})
	}
}

// TestProviderCredential pins the credential lookup, including the Gemini alias
// branch and the no-key-needed case.
//
// The alias branch is the one that silently breaks CI: a learner who exported
// GEMINI_API_KEY (the name pimodels.APIKeyEnvVar reports) would otherwise be
// told "provider not configured" while a live key sits in their environment.
func TestProviderCredential(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
		ok   bool
	}{
		{
			name: "GOOGLE_API_KEY wins when both Gemini names are set",
			env:  map[string]string{"GOOGLE_API_KEY": "google", "GEMINI_API_KEY": "gemini"},
			want: "google",
			ok:   true,
		},
		{
			name: "GEMINI_API_KEY alone is accepted",
			env:  map[string]string{"GEMINI_API_KEY": "gemini"},
			want: "gemini",
			ok:   true,
		},
		{
			name: "an empty variable does not count as configured",
			env:  map[string]string{"GOOGLE_API_KEY": ""},
			ok:   false,
		},
		{
			name: "an OpenAI key is found",
			env:  map[string]string{"OPENAI_API_KEY": "sk-x"},
			want: "sk-x",
			ok:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCredentials(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			d, _ := resolveProvider("gemini")
			if tt.name == "an OpenAI key is found" {
				d, _ = resolveProvider("openai")
			}

			got, ok := providerCredential(d)
			if ok != tt.ok {
				t.Fatalf("providerCredential(%s) ok = %v, want %v", d.Provider, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("providerCredential(%s) = %q, want %q", d.Provider, got, tt.want)
			}
		})
	}
}

// TestProviderCredentialNeedsNoKey pins that a provider with no EnvVar is
// treated as configured rather than skipped — the branch that keeps a
// key-free backend (a local gateway) selectable at all.
func TestProviderCredentialNeedsNoKey(t *testing.T) {
	clearCredentials(t)
	got, ok := providerCredential(providerDefault{Provider: "local", Model: "m"})
	if !ok {
		t.Fatal("a provider with no EnvVar should count as configured")
	}
	if got != "" {
		t.Errorf("credential = %q, want empty", got)
	}
}

// TestLoadModel pins the single entry point main uses.
//
// It asserts both halves of the contract: the choice it reports matches the
// environment, and it returns a usable model. The unknown-provider case is the
// error path — it must fail before constructing anything, so a typo in
// DEFAULT_MODEL_PROVIDER is reported as a typo rather than as a provider error.
func TestLoadModel(t *testing.T) {
	t.Run("unknown provider fails loudly", func(t *testing.T) {
		clearCredentials(t)
		t.Setenv("DEFAULT_MODEL_PROVIDER", "not-a-provider")

		m, _, err := LoadModel(context.Background())
		if err == nil {
			t.Fatal("LoadModel() error = nil, want an error for an unknown provider")
		}
		if m != nil {
			t.Error("LoadModel() returned a model alongside the error")
		}
		if !strings.Contains(err.Error(), "невідомий провайдер") {
			t.Errorf("error = %q, want it to name the unknown provider", err)
		}
	})

	t.Run("a named provider yields a model and a matching choice", func(t *testing.T) {
		clearCredentials(t)
		t.Setenv("OPENAI_API_KEY", "sk-test-not-a-real-key")
		t.Setenv("DEFAULT_MODEL_PROVIDER", "openai")

		m, choice, err := LoadModel(context.Background())
		if err != nil {
			t.Fatalf("LoadModel() error: %v", err)
		}
		if m == nil {
			t.Fatal("LoadModel() returned a nil model")
		}
		if choice.Provider != "openai" {
			t.Errorf("choice.Provider = %q, want openai", choice.Provider)
		}
		if choice.Model != "gpt-5.6-luna" {
			t.Errorf("choice.Model = %q, want gpt-5.6-luna", choice.Model)
		}
		if choice.Reason == "" {
			t.Error("choice.Reason is empty; it is what tells a learner which backend answered")
		}
	})

	// The regression this pins: with the retry unconditional, naming a provider
	// that has no key produced a working model built as `ollama/<vendor-model>`
	// and a 404 from the local Ollama daemon on the first request. The learner
	// sees a model-not-found for a model they never asked for, and the real
	// cause — no key — is never mentioned.
	t.Run("a named provider with no key reports the key, not an ollama 404", func(t *testing.T) {
		clearCredentials(t)
		t.Setenv("DEFAULT_MODEL_PROVIDER", "gemini")

		m, _, err := LoadModel(context.Background())
		if err == nil {
			t.Fatal("LoadModel() error = nil, want the missing-key error to survive")
		}
		if m != nil {
			t.Error("LoadModel() returned a model alongside the error")
		}
		if !strings.Contains(err.Error(), "GEMINI_API_KEY") {
			t.Errorf("error = %q, want it to name GEMINI_API_KEY", err)
		}
		if strings.Contains(err.Error(), "ollama") {
			t.Errorf("error = %q, want no mention of ollama: the retry must not apply to a routable name", err)
		}
	})

	// The case the retry was written for, pinned so narrowing it cannot quietly
	// delete the feature: a bare local tag pimodels cannot route still becomes
	// ollama/<tag>.
	t.Run("a bare local tag is still retried with the ollama prefix", func(t *testing.T) {
		clearCredentials(t)
		t.Setenv("DEFAULT_MODEL_PROVIDER", "ollama")
		t.Setenv("OLLAMA_MODEL", "qwen3.5:4b-mlx")

		m, _, err := LoadModel(context.Background())
		if err != nil {
			t.Fatalf("LoadModel() error: %v", err)
		}
		if m == nil {
			t.Fatal("LoadModel() returned a nil model")
		}
	})
}
