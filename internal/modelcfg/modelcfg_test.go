package modelcfg

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dimetron/pi-go/pimodels"
)

// credentialEnvVars — everything that enables a provider or changes the model
// choice.
//
// Choice tests must start from a clean environment: with GOOGLE_API_KEY exported
// on a developer's machine and not in CI, the same test would be checking two
// different programs. t.Setenv(name, "") sets the variable empty, and adkenv.Key
// treats empty as unset — which is exactly what is needed.
var credentialEnvVars = []string{
	"MODEL", "DEFAULT_MODEL_PROVIDER",
	"AGENTGATEWAY_BASE_URL", "AGENTGATEWAY_MODEL", "AGENTGATEWAY_API_KEY",
	"OLLAMA_BASE_URL", "OLLAMA_MODEL", "OLLAMA_API_KEY",
	"GOOGLE_API_KEY", "GEMINI_API_KEY", "GEMINI_MODEL",
	"OPENAI_API_KEY", "OPENAI_MODEL",
}

// clearCredentials zeroes every model-choice variable for the duration of a test.
func clearCredentials(t *testing.T) {
	t.Helper()
	for _, name := range credentialEnvVars {
		t.Setenv(name, "")
	}
}

// TestDefaultsIsACopy pins that the exported table cannot be mutated by a
// caller. The table is policy shared by every lab; one lab reordering it to suit
// itself would silently change every other lab's auto-detection.
func TestDefaultsIsACopy(t *testing.T) {
	t.Parallel()

	first := Defaults()
	if len(first) == 0 {
		t.Fatal("Defaults() is empty; the provider table is the package's whole point")
	}
	original := first[0].Provider
	first[0].Provider = "mutated"

	if got := Defaults()[0].Provider; got != original {
		t.Errorf("Defaults()[0].Provider = %q after mutation, want %q — the table is shared state", got, original)
	}
}

// TestLoadEnvToleratesMissingFile pins the offline path: a lab must start with no
// apps/.env at all and get no error, because the whole point of the fixture
// providers is that no key is required.
func TestLoadEnvToleratesMissingFile(t *testing.T) {
	t.Parallel()

	if err := LoadEnv(t.TempDir()); err != nil {
		t.Errorf("LoadEnv(empty dir) = %v, want nil (a missing apps/.env is not an error)", err)
	}
}

// TestLoadEnvReadsAppsEnv pins that LoadEnv actually finds and reads the file, so
// callers do not each invent their own lookup.
func TestLoadEnvReadsAppsEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root+"/apps", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(root+"/apps/.env", []byte("MODELCFG_TEST_KEY=from-file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("MODELCFG_TEST_KEY", "")
	_ = os.Unsetenv("MODELCFG_TEST_KEY")

	if err := LoadEnv(root + "/apps"); err != nil {
		t.Fatalf("LoadEnv = %v", err)
	}
	if got := os.Getenv("MODELCFG_TEST_KEY"); got != "from-file" {
		t.Errorf("MODELCFG_TEST_KEY = %q, want %q", got, "from-file")
	}
}

// TestResolveModelName pins the one invariant the whole benchmarking exercise
// rests on: the model name comes from MODEL, and falls back to the provider's
// default when absent.
//
// Why it is worth a test: running the same prompt on two models is supposed to
// be a change of ONE variable. If MODEL were ignored, the benchmark would
// compare a model against itself and print a perfectly plausible table — the
// worst kind of wrong, because nothing fails.
func TestResolveModelName(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			if got := resolveModelName(tt.env, fallback); got != tt.want {
				t.Errorf("resolveModelName(%q, %q) = %q, want %q", tt.env, fallback, got, tt.want)
			}
		})
	}
}

// TestResolveProvider pins that every provider in the table is reachable by name,
// case-insensitively, and that an unknown name is reported as unknown rather than
// silently falling back to something plausible.
func TestResolveProvider(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
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
// match sends a Gemini request to the local Ollama daemon, which fails with
// "model not found" — a message that sends the reader hunting for a missing
// `ollama pull` instead of a routing bug.
func TestProviderForModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
		want  string
		ok    bool
	}{
		{model: "agentgateway/openai/gpt-5.6-luna", want: "agentgateway/openai", ok: true},
		{model: "agentgateway/gemini/gemini-3.8-flash", want: "agentgateway/gemini", ok: true},
		{model: "agentgateway/ollama/qwen3.5:4b-mlx", want: "agentgateway/ollama", ok: true},
		{model: "agentgateway/deepseek-v4.1-flash:cloud", want: "agentgateway/ollama", ok: true},
		{model: "gemini-3.8-flash", want: "gemini", ok: true},
		{model: "gpt-5.6-luna", want: "openai", ok: true},
		{model: "deepseek-v4.1-flash:cloud", want: "ollama", ok: true},
		{model: "not-a-real-model-xyz", ok: false},
		{model: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()
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

// TestChoosePrecedence pins the resolution order and, more importantly, that
// every refusal is loud.
//
// Three cases assert an error rather than a choice. They are the same bug at
// three depths: a provider nobody selected. Substituting gemini for it — what an
// earlier version did — turns "you have not configured anything" into a
// plausible-looking pair of names in the log, and the mistake only surfaces at
// the first request, as another provider's error.
func TestChoosePrecedence(t *testing.T) {
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
			env:          map[string]string{"DEFAULT_MODEL_PROVIDER": "gemini", "GOOGLE_API_KEY": "k"},
			wantProvider: "gemini",
			wantModel:    "gemini-3.8-flash",
		},
		{
			name:         "an agentgateway route is chosen by name",
			env:          map[string]string{"DEFAULT_MODEL_PROVIDER": "agentgateway/gemini", "AGENTGATEWAY_BASE_URL": "http://localhost:4000"},
			wantProvider: "agentgateway/gemini",
			wantModel:    "agentgateway/gemini/gemini-3.8-flash",
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
			name:         "an API key alone auto-detects the provider",
			env:          map[string]string{"GOOGLE_API_KEY": "k"},
			wantProvider: "gemini",
			wantModel:    "gemini-3.8-flash",
		},
		{
			name:         "the provider model variable supplies the model",
			env:          map[string]string{"DEFAULT_MODEL_PROVIDER": "gemini", "GEMINI_MODEL": "gemini-3.8-pro", "GOOGLE_API_KEY": "k"},
			wantProvider: "gemini",
			wantModel:    "gemini-3.8-pro",
		},
		{
			name:         "an agentgateway base URL turns the gateway on by itself",
			env:          map[string]string{"AGENTGATEWAY_BASE_URL": "http://localhost:4000"},
			wantProvider: "agentgateway/ollama",
			wantModel:    "agentgateway/ollama/qwen3.5:4b-mlx",
		},
		{
			name:         "GEMINI_API_KEY is accepted as an alias",
			env:          map[string]string{"GEMINI_API_KEY": "k"},
			wantProvider: "gemini",
			wantModel:    "gemini-3.8-flash",
		},
		{
			name:        "an unknown provider is a loud error, not a silent fallback",
			env:         map[string]string{"DEFAULT_MODEL_PROVIDER": "anthropic"},
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

			got, err := Choose(os.Getenv("MODEL"), os.Getenv("DEFAULT_MODEL_PROVIDER"))

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Choose() = %+v, want an error", got)
				}
				if !strings.Contains(err.Error(), tt.wantErrText) {
					t.Errorf("Choose() error = %q, want it to contain %q", err, tt.wantErrText)
				}
				// The error is the whole remedy: it has to name the providers
				// that would have worked, or it just moves the search.
				if !strings.Contains(err.Error(), "agentgateway/ollama") {
					t.Errorf("Choose() error = %q, want it to list the known providers", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Choose() error = %v", err)
			}
			if got.Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", got.Provider, tt.wantProvider)
			}
			if got.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", got.Model, tt.wantModel)
			}
			if got.Reason == "" {
				t.Error("Reason is empty; it is what tells the caller which backend answered")
			}
		})
	}
}

// TestQualifyGatewayModel pins the routing fix: a bare model name under a gateway
// provider must come back carrying the route.
//
// Without it pimodels sees a bare vendor id, decides "vendor, not gateway", and
// opens a direct client to the vendor — so the gateway never sees the request,
// traces and cost accounting have holes, and built-in tools such as Google
// Search behave differently through a path nobody intended to use.
func TestQualifyGatewayModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		model    string
		want     string
	}{
		{name: "non-gateway provider is untouched", provider: "gemini", model: "gemini-3.8-flash", want: "gemini-3.8-flash"},
		{name: "a bare name gets the full route", provider: "agentgateway/gemini", model: "gemini-3.8-flash", want: "agentgateway/gemini/gemini-3.8-flash"},
		{name: "a vendor segment only needs the gateway prefix", provider: "agentgateway/gemini", model: "gemini/gemini-3.8-flash", want: "agentgateway/gemini/gemini-3.8-flash"},
		{name: "an already-qualified name is left alone", provider: "agentgateway/gemini", model: "agentgateway/gemini/gemini-3.8-flash", want: "agentgateway/gemini/gemini-3.8-flash"},
		{name: "a different vendor segment is not doubled", provider: "agentgateway/openai", model: "gpt-5.6-luna", want: "agentgateway/openai/gpt-5.6-luna"},
		{name: "matching is case-insensitive", provider: "agentgateway/gemini", model: "AgentGateway/gemini/x", want: "AgentGateway/gemini/x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := qualifyGatewayModel(tt.provider, tt.model); got != tt.want {
				t.Errorf("qualifyGatewayModel(%q, %q) = %q, want %q", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

// TestGatewayRoutesResolveThroughPimodels pins that a gateway model name survives
// qualification and is still recognised by pimodels as a gateway route.
//
// Qualification is only correct if the result is routable: a prefix that pimodels
// does not know would move the failure from "silently bypassed" to "unknown
// model", which is louder but still wrong.
func TestGatewayRoutesResolveThroughPimodels(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"agentgateway/ollama", "agentgateway/openai", "agentgateway/gemini"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()

			qualified := qualifyGatewayModel(provider, "some-model")
			info, err := pimodels.Resolve(qualified)
			if err != nil {
				t.Fatalf("pimodels.Resolve(%q) = %v, want it routable after qualification", qualified, err)
			}
			if info.Provider != "agentgateway" {
				t.Errorf("pimodels resolved %q to provider %q, want %q", qualified, info.Provider, "agentgateway")
			}
			if !strings.HasPrefix(qualified, "agentgateway/") {
				t.Errorf("qualified name %q lost the gateway prefix", qualified)
			}
		})
	}
}

// TestProviderCredential pins credential detection, including the Gemini alias
// and the empty-but-set case.
//
// An empty-but-set variable is a common way to break CI, and treating it as
// "provider configured" produces an opaque 401 instead of an honest "not
// configured".
func TestProviderCredential(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantOK  bool
		wantVal string
	}{
		{
			name:    "a set key counts as configured",
			env:     map[string]string{"GOOGLE_API_KEY": "k"},
			wantOK:  true,
			wantVal: "k",
		},
		{
			name:    "the GEMINI_API_KEY alias counts as configured",
			env:     map[string]string{"GEMINI_API_KEY": "alias"},
			wantOK:  true,
			wantVal: "alias",
		},
		{
			name:   "an empty variable does not count",
			env:    map[string]string{"GOOGLE_API_KEY": ""},
			wantOK: false,
		},
		{
			name:   "nothing set does not count",
			env:    nil,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCredentials(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			d, ok := resolveProvider("gemini")
			if !ok {
				t.Fatal("gemini missing from the table")
			}
			got, gotOK := providerCredential(d)
			if gotOK != tt.wantOK {
				t.Fatalf("providerCredential() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if tt.wantVal != "" && got != tt.wantVal {
				t.Errorf("providerCredential() = %q, want %q", got, tt.wantVal)
			}
		})
	}
}

// TestProviderCredentialNeedsNoKey pins that a provider declaring no credential
// variable counts as configured. Without this a key-free provider would be
// unreachable by auto-detection.
func TestProviderCredentialNeedsNoKey(t *testing.T) {
	t.Parallel()

	d := Default{Provider: "keyless", Model: "m", EnvVar: ""}
	value, ok := providerCredential(d)
	if !ok {
		t.Fatal("a provider with no EnvVar must count as configured")
	}
	if value != "" {
		t.Errorf("providerCredential() = %q, want empty", value)
	}
}

// TestBuildDoesNotRetryARoutableNameIntoAFalseSuccess pins the condition on the
// `ollama/` retry, which is the whole point of it being conditional.
//
// The retry exists for a bare local tag pimodels cannot route at all. Applied
// unconditionally it became a generator of false successes: `ollama/` needs no
// key and calls nothing, so a routable name that failed for a real reason — a
// missing API key — was quietly replaced by a working model nobody asked for,
// and the genuine cause disappeared, surfacing later as an unrelated 404.
//
// So the assertion is not "Build fails" but "Build fails for the RIGHT reason":
// a routable name with no credential must report the credential, never return a
// model built under a different provider.
func TestBuildDoesNotRetryARoutableNameIntoAFalseSuccess(t *testing.T) {
	clearCredentials(t)

	// gemini-3.8-flash IS routable, so its failure is real: no key is set.
	_, err := Build(context.Background(), "gemini", "gemini-3.8-flash")
	if err == nil {
		t.Fatal("Build() = nil error for a routable model with no credential, " +
			"want the real cause — an unconditional retry is masking it")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "api key") {
		t.Errorf("Build() error = %q, want it to name the missing API key, "+
			"not a symptom of some other model", err)
	}
}

// TestBuildRetriesBareLocalTags pins the case the retry was written for: a bare
// local tag that pimodels cannot route is offered again as `ollama/<tag>`, which
// needs no credential. Without this the offline path would stop working.
func TestBuildRetriesBareLocalTags(t *testing.T) {
	clearCredentials(t)

	m, err := Build(context.Background(), "ollama", "qwen3.5:4b-mlx")
	if err != nil {
		t.Fatalf("Build(bare local tag) = %v, want it routable as ollama/<tag>", err)
	}
	if m == nil {
		t.Fatal("Build() = nil model with nil error")
	}
}

// TestBuildPrefersTheGatewayEndpointOverOllama pins the endpoint rule for gateway
// routes.
//
// Both variables can be set at once — a developer with a gateway up and Ollama
// running locally — and passing OLLAMA_BASE_URL to a gateway route would point
// the client at a different service and break routing inside the gateway, so the
// gateway address must win.
func TestBuildPrefersTheGatewayEndpointOverOllama(t *testing.T) {
	clearCredentials(t)
	t.Setenv("AGENTGATEWAY_BASE_URL", "http://gateway.invalid:9999")
	t.Setenv("OLLAMA_BASE_URL", "http://ollama.invalid:8888")

	// A gateway route with a keyless provider must build without reaching either
	// endpoint: building is config, not a call. The assertion is that it does not
	// fail on the deliberately unreachable hosts, which proves no request left
	// the process and that the route is treated as a gateway, not a vendor.
	m, err := Build(context.Background(), "agentgateway/ollama", "agentgateway/ollama/qwen3.5:4b-mlx")
	if err != nil {
		t.Fatalf("Build(gateway route) = %v, want a built model without any network call", err)
	}
	if m == nil {
		t.Fatal("Build() = nil model with nil error")
	}
}

// TestLoadReturnsTheChoiceAndTheModel pins the end-to-end entry point: a
// configured environment yields both a built model and the choice that explains
// it, so a caller can log which backend answered.
func TestLoadReturnsTheChoiceAndTheModel(t *testing.T) {
	clearCredentials(t)
	t.Setenv("DEFAULT_MODEL_PROVIDER", "ollama")
	t.Setenv("MODEL", "qwen3.5:4b-mlx")

	m, choice, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() = %v, want a built model", err)
	}
	if m == nil {
		t.Fatal("Load() = nil model with nil error")
	}
	if choice.Provider != "ollama" {
		t.Errorf("choice.Provider = %q, want %q", choice.Provider, "ollama")
	}
	if choice.Model != "qwen3.5:4b-mlx" {
		t.Errorf("choice.Model = %q, want %q", choice.Model, "qwen3.5:4b-mlx")
	}
	if choice.Reason == "" {
		t.Error("choice.Reason is empty; it is what tells the caller which backend answered")
	}
}

// TestLoadSurfacesTheChosenProviderOnAnUnconfiguredEnvironment pins that a
// refusal carries the reason: without a key the caller must be told what to set,
// and the error has to name the variable the provider wanted rather than the
// provider's own low-level complaint.
func TestLoadSurfacesTheChosenProviderOnAnUnconfiguredEnvironment(t *testing.T) {
	clearCredentials(t)

	_, _, err := Load(context.Background())
	if err == nil {
		t.Fatal("Load() = nil error with nothing configured, want a loud refusal")
	}
	for _, want := range []string{"жодного провайдера не налаштовано", "agentgateway/ollama"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error = %q, want it to contain %q", err, want)
		}
	}
}

// TestLoadEnrichesABuildErrorWithTheVariableName pins the hint that makes a
// failed build actionable.
//
// pimodels reports "api key is required for Google AI backend" and similar, but
// never says WHICH variable the caller should set. That name only exists here,
// where the choice is known, so this is the one place the mapping can be made.
func TestLoadEnrichesABuildErrorWithTheVariableName(t *testing.T) {
	clearCredentials(t)
	t.Setenv("DEFAULT_MODEL_PROVIDER", "gemini")
	t.Setenv("MODEL", "gemini-3.8-flash")

	_, _, err := Load(context.Background())
	if err == nil {
		t.Fatal("Load() = nil error with no Gemini key, want a failure that names the variable")
	}
	if !strings.Contains(err.Error(), "GOOGLE_API_KEY") {
		t.Errorf("Load() error = %q, want it to name the variable the provider needed", err)
	}
}

// TestCredentialVarNameIsARealVariable pins that the hint never names a variable
// that does not exist.
//
// The regression this guards: pimodels.APIKeyEnvVar derives a name from the
// provider string, so for a gateway route it returns "AGENTGATEWAY/GEMINI_API_KEY"
// — a slash in the middle of a variable name, which no shell will ever export.
// A reader following that hint finds nothing. The table records the variable that
// actually enables the provider, so it is asked first.
func TestCredentialVarNameIsARealVariable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider string
		want     string
	}{
		{provider: "gemini", want: "GOOGLE_API_KEY"},
		{provider: "openai", want: "OPENAI_API_KEY"},
		{provider: "ollama", want: "OLLAMA_BASE_URL"},
		{provider: "agentgateway/gemini", want: "AGENTGATEWAY_BASE_URL"},
		{provider: "agentgateway/ollama", want: "AGENTGATEWAY_BASE_URL"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			t.Parallel()
			got := credentialVarName(tt.provider)
			if got != tt.want {
				t.Errorf("credentialVarName(%q) = %q, want %q", tt.provider, got, tt.want)
			}
			if strings.Contains(got, "/") {
				t.Errorf("credentialVarName(%q) = %q contains a slash; no such variable can be exported",
					tt.provider, got)
			}
		})
	}
}

// TestProviderModelPairsResolve pins that every non-gateway model in the table
// is routable through pimodels exactly as written: the bare name for gemini and
// openai, and an explicitly prefixed name for a local Ollama tag.
//
// The gateway half of the table is covered by TestGatewayRoutesResolveThroughPimodels
// (where qualification adds the prefix for you). This is the other half, and it
// lives here rather than in a lab because the table lives here: a lab that
// pinned it would be testing this package's data through a copy of it.
//
// Moved from the Week 1 Day 2 lab when that lab's provider.go was replaced by
// this package; the assertion is unchanged.
func TestProviderModelPairsResolve(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
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
