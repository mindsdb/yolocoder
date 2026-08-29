package app

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mindsdb/yolocoder/internal/auth"
	"github.com/mindsdb/yolocoder/internal/config"
	"golang.org/x/term"
)

const Help = `YoloCoder

Usage:
  yolocoder                       connect an LLM provider, then start
  yolocoder --llm-from-env-vars   use OPENAI_* environment variables
  yolocoder config show           show the saved provider
  yolocoder config connect        replace the saved provider
  yolocoder config reset          remove the saved provider
  yolocoder version               show the version

Environment provider:
  OPENAI_BASE_URL                 OpenAI-compatible endpoint
  OPENAI_API_KEY                  endpoint API key
  OPENAI_MODEL                    optional model name
`

func EnsureLLM() error {
	_, configured, err := config.Load()
	if err != nil {
		return err
	}
	if configured {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
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
		fmt.Printf("API key: configured\nConfig: %s\nCredentials: %s\n", configPath, credentialsPath)
		return 0
	case "connect":
		if !term.IsTerminal(int(os.Stdin.Fd())) {
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

func connect(input *os.File, output *os.File) (config.LLM, error) {
	reader := bufio.NewReader(input)
	fmt.Fprintln(output, "Connect an LLM inference provider")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "  1. MindsHub (recommended) — sign in with your browser")
	fmt.Fprintln(output, "  2. Other — any OpenAI-compatible endpoint")
	fmt.Fprintln(output)
	fmt.Fprint(output, "> ")
	choice, err := reader.ReadString('\n')
	if err != nil {
		return config.LLM{}, fmt.Errorf("read provider choice: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "1", "mindshub", "minds hub":
		return connectMindsHub(output, reader, input)
	case "2", "other", "custom":
		return connectOther(output, reader, input)
	default:
		return config.LLM{}, fmt.Errorf("choose 1 for MindsHub or 2 for Other")
	}
}

func connectMindsHub(output *os.File, reader *bufio.Reader, input *os.File) (config.LLM, error) {
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
		apiKey, err = promptAPIKey(output, reader, input)
		if err != nil {
			return config.LLM{}, err
		}
	}
	provider := config.LLM{Provider: "mindshub", BaseURL: config.MindsHubBaseURL(), APIKey: apiKey, Model: "mindshub_air"}
	return saveProvider(output, provider)
}

func connectOther(output *os.File, reader *bufio.Reader, input *os.File) (config.LLM, error) {
	fmt.Fprint(output, "OpenAI-compatible base URL\n> ")
	baseURL, err := reader.ReadString('\n')
	if err != nil {
		return config.LLM{}, fmt.Errorf("read base URL: %w", err)
	}
	baseURL, err = config.ValidateBaseURL(baseURL)
	if err != nil {
		return config.LLM{}, err
	}
	apiKey, err := promptAPIKey(output, reader, input)
	if err != nil {
		return config.LLM{}, err
	}
	fmt.Fprint(output, "Default model (optional)\n> ")
	model, err := reader.ReadString('\n')
	if err != nil {
		return config.LLM{}, fmt.Errorf("read model: %w", err)
	}
	provider := config.LLM{Provider: "openai-compatible", BaseURL: baseURL, APIKey: apiKey, Model: strings.TrimSpace(model)}
	return saveProvider(output, provider)
}

func promptAPIKey(output *os.File, reader *bufio.Reader, input *os.File) (string, error) {
	fmt.Fprint(output, "API key\n> ")
	var value string
	if term.IsTerminal(int(input.Fd())) {
		secret, err := term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(output)
		if err != nil {
			return "", err
		}
		value = string(secret)
	} else {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		value = line
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
