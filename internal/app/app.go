package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mindsdb/yolocoder/internal/agent"
	"github.com/mindsdb/yolocoder/internal/auth"
	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/terminal"
)

const Help = `YoloCoder

Usage:
  yolocoder <task>                make and test a coding change, then exit
  yolocoder                       start an interactive session
  yolocoder --context <text> <task>
                                  add background for the run; repeatable,
                                  and "-" reads it from stdin. A one-shot
                                  run is told nothing else about this
                                  folder, so this is how a script supplies
                                  what an interactive session would recall.
  yolocoder --allow-commands <task>
                                  run any generated command without
                                  asking. By default a command that only
                                  reads, and stays inside this folder,
                                  runs on its own; anything else is shown
                                  and confirmed, and a run with nobody at
                                  the terminal will not run one at all.
  yolocoder --confirm-commands <task>
                                  ask about every command, read-only ones
                                  included.
  yolocoder --llm-from-env-vars <task>
                                  use OPENAI_* environment variables
  yolocoder config show           show the saved provider
  yolocoder config connect        replace the saved provider
  yolocoder config reset          remove the saved provider
  yolocoder model                 pick a model from the endpoint's list
  yolocoder model <name>          set the model directly
  yolocoder update                immediately check and install an update
  yolocoder version               show the version

Environment provider:
  OPENAI_BASE_URL                 OpenAI-compatible endpoint
  OPENAI_API_KEY                  endpoint API key
  OPENAI_MODEL                    optional model name
  OPENAI_API_DIALECT              "chat" for /v1/chat/completions,
                                  otherwise the Responses API
`

const listModelsTimeout = 10 * time.Second
const detectAPITimeout = 10 * time.Second
const defaultMindsHubModel = "mindshub_air"

func terminalInput() bool {
	return terminal.IsTTY(os.Stdin)
}

func EnsureLLM() error {
	_, configured, err := config.Load()
	if err != nil {
		return err
	}
	if configured {
		return nil
	}
	if !terminal.IsTTY(os.Stdin) {
		return fmt.Errorf("no LLM inference provider is configured\n\nRun yolocoder in an interactive terminal, or use --llm-from-env-vars")
	}
	_, err = connect(os.Stdin, os.Stdout)
	return err
}

func UseLLMFromEnvironment() error {
	_, err := config.FromEnvironment(os.Getenv)
	return err
}

func RunConfig(args []string) int {
	if len(args) == 0 {
		args = []string{"connect"}
	}
	switch args[0] {
	case "show":
		provider, configured, err := config.Load()
		if err != nil {
			return fail(err)
		}
		if !configured {
			fmt.Println("No LLM inference provider configured.")
			return 0
		}
		configPath, credentialsPath, err := config.Paths()
		if err != nil {
			return fail(err)
		}
		fmt.Printf("Provider: %s\nEndpoint: %s\n", provider.Provider, provider.BaseURL)
		if provider.Model != "" {
			fmt.Printf("Model: %s\n", provider.Model)
		}
		if provider.API == config.APIChat {
			fmt.Println("API: chat completions")
		} else {
			fmt.Println("API: responses")
		}
		fmt.Printf("API key: configured\nConfig: %s\nCredentials: %s\n", configPath, credentialsPath)
		return 0
	case "connect":
		if !terminal.IsTTY(os.Stdin) {
			return fail(fmt.Errorf("yolocoder config connect requires an interactive terminal"))
		}
		_, err := connect(os.Stdin, os.Stdout)
		if err != nil {
			return fail(err)
		}
		return 0
	case "reset":
		if err := config.Reset(); err != nil {
			return fail(err)
		}
		fmt.Println("YoloCoder configuration reset.")
		return 0
	default:
		return fail(fmt.Errorf("unknown config command %q\n\nUse: yolocoder config [show|connect|reset]", args[0]))
	}
}

// RunModel changes the model of the already-saved provider. With an
// argument, it sets the model directly. Without one, it lists the models
// the endpoint's OpenAI-compatible /v1/models offers (this is what makes
// `yolocoder model` useful with MindsHub, which exposes several) and lets
// the user pick one; if listing isn't supported, it falls back to typing a
// name.
func RunModel(args []string) int {
	provider, configured, err := config.Load()
	if err != nil {
		return fail(err)
	}
	if !configured {
		return fail(fmt.Errorf("no LLM inference provider configured; run `yolocoder config connect` first"))
	}
	var model string
	if len(args) > 0 {
		model = strings.TrimSpace(strings.Join(args, " "))
		if model == "" {
			return fail(fmt.Errorf("model name is required"))
		}
	} else {
		if !terminal.IsTTY(os.Stdin) {
			return fail(fmt.Errorf("yolocoder model requires an interactive terminal, or pass a model name: yolocoder model <name>"))
		}
		reader := terminal.NewReader(os.Stdin)
		model, err = pickModel(os.Stdout, reader, provider)
		if err != nil {
			return fail(err)
		}
	}
	provider.Model = model
	if err := config.Save(provider); err != nil {
		return fail(err)
	}
	fmt.Printf("Model set to %s.\n", model)
	return 0
}

// pickModel lists provider's available models and lets the user select
// one, falling back to a free-text prompt if the endpoint doesn't support
// listing them.
func pickModel(output *os.File, reader *terminal.Reader, provider config.LLM) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listModelsTimeout)
	defer cancel()
	models, err := agent.ListModels(ctx, provider.BaseURL, provider.APIKey)
	if err != nil || len(models) == 0 {
		fmt.Fprint(output, "Model\n> ")
		typed, readErr := reader.ReadLine()
		if readErr != nil {
			return "", fmt.Errorf("read model: %w", readErr)
		}
		typed = strings.TrimSpace(typed)
		if typed == "" {
			return "", fmt.Errorf("model is required")
		}
		return typed, nil
	}
	choices := make([]terminal.Choice, len(models))
	initial := 0
	for index, model := range models {
		choices[index] = terminal.Choice{Label: model}
		if model == provider.Model {
			initial = index
		}
	}
	fmt.Fprintln(output, "Choose a model")
	fmt.Fprintln(output)
	selected, err := reader.Select(output, choices, initial)
	if err != nil {
		return "", err
	}
	return models[selected], nil
}

func connect(input *os.File, output *os.File) (config.LLM, error) {
	reader := terminal.NewReader(input)
	fmt.Fprintln(output, "Connect an LLM inference provider")
	fmt.Fprintln(output)
	choice, err := reader.Select(output, []terminal.Choice{
		{Label: "MindsHub (recommended)", Detail: "sign in with your browser"},
		{Label: "Custom", Detail: "any OpenAI-compatible endpoint"},
	}, 0)
	if err != nil {
		return config.LLM{}, err
	}
	switch choice {
	case 0:
		return connectMindsHub(output, reader)
	case 1:
		return connectOther(output, reader)
	default:
		return config.LLM{}, fmt.Errorf("invalid provider selection")
	}
}

func connectMindsHub(output *os.File, reader *terminal.Reader) (config.LLM, error) {
	fmt.Fprintln(output, "Opening your browser to sign in to MindsHub...")
	ctx, cancel := context.WithTimeout(context.Background(), auth.DefaultTimeout)
	defer cancel()
	token, err := auth.Login(ctx, config.MindsHubOIDC(), func(url string) {
		fmt.Fprintf(output, "If the browser does not open, visit:\n%s\n", url)
	})
	var apiKey string
	if err == nil {
		apiKey, err = auth.CreateAPIKey(ctx, config.MindsHubAuthAPI(), token, "yolocoder")
	}
	if err != nil {
		fmt.Fprintf(output, "Browser sign-in could not complete: %v\n", err)
		fmt.Fprintln(output, "Paste a MindsHub API key instead.")
		apiKey, err = promptAPIKey(output, reader)
		if err != nil {
			return config.LLM{}, err
		}
	}
	provider := config.LLM{Provider: "mindshub", BaseURL: config.MindsHubBaseURL(), APIKey: apiKey}
	model, err := pickModel(output, reader, provider)
	if err != nil {
		fmt.Fprintf(output, "Could not choose a model (%v); using the default.\n", err)
		model = defaultMindsHubModel
	}
	provider.Model = model
	return saveProvider(output, provider)
}

func connectOther(output *os.File, reader *terminal.Reader) (config.LLM, error) {
	fmt.Fprint(output, "OpenAI-compatible base URL\n> ")
	baseURL, err := reader.ReadLine()
	if err != nil {
		return config.LLM{}, fmt.Errorf("read base URL: %w", err)
	}
	baseURL, err = config.ValidateBaseURL(baseURL)
	if err != nil {
		return config.LLM{}, err
	}
	apiKey, err := promptAPIKey(output, reader)
	if err != nil {
		return config.LLM{}, err
	}
	fmt.Fprint(output, "Model\n> ")
	model, err := reader.ReadLine()
	if err != nil {
		return config.LLM{}, fmt.Errorf("read model: %w", err)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return config.LLM{}, fmt.Errorf("model is required")
	}
	provider := config.LLM{Provider: "openai-compatible", BaseURL: baseURL, APIKey: apiKey, Model: model}
	provider.API = detectAPI(output, provider)
	return saveProvider(output, provider)
}

// detectAPI works out which dialect an endpoint speaks and says so, since
// "OpenAI-compatible" covers both the Responses API and the far more
// common chat completions. Falling back to Responses on a failed probe
// keeps the previous behavior for an endpoint that is simply unreachable
// at connect time.
func detectAPI(output *os.File, provider config.LLM) string {
	ctx, cancel := context.WithTimeout(context.Background(), detectAPITimeout)
	defer cancel()
	dialect, err := agent.DetectAPI(ctx, provider.BaseURL, provider.APIKey)
	if err != nil {
		fmt.Fprintf(output, "Could not check which API the endpoint offers (%v); assuming the Responses API.\n", err)
		return config.APIResponses
	}
	if dialect == config.APIChat {
		fmt.Fprintln(output, "This endpoint offers chat completions rather than the Responses API; using that.")
	}
	return dialect
}

func promptAPIKey(output *os.File, reader *terminal.Reader) (string, error) {
	value, err := reader.ReadPassword(output)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("API key is required")
	}
	return value, nil
}

func saveProvider(output *os.File, provider config.LLM) (config.LLM, error) {
	if err := config.Save(provider); err != nil {
		return config.LLM{}, err
	}
	configPath, _, _ := config.Paths()
	fmt.Fprintf(output, "LLM provider connected.\nSaved configuration to %s\n", configPath)
	return provider, nil
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, err)
	return 1
}
