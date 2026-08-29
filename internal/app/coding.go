package app

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mindsdb/yolocoder/internal/agent"
	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/repo"
)

func PrepareTask(task string, fromEnvironment bool) (string, config.LLM, error) {
	provider, err := resolveProvider(fromEnvironment)
	if err != nil {
		return "", config.LLM{}, err
	}
	if strings.TrimSpace(task) == "" {
		task, err = promptTask()
		if err != nil {
			return "", config.LLM{}, err
		}
	}
	return task, provider, nil
}

func RunTask(ctx context.Context, task string, provider config.LLM, progress func(string)) error {
	repository, err := repo.Open(".")
	if err != nil {
		return err
	}
	client, err := agent.NewClient(provider)
	if err != nil {
		return err
	}
	return agent.NewRunner(client, repository).Run(ctx, task, progress)
}

func resolveProvider(fromEnvironment bool) (config.LLM, error) {
	if fromEnvironment {
		return config.FromEnvironment(os.Getenv)
	}
	provider, configured, err := config.Load()
	if err != nil {
		return config.LLM{}, err
	}
	if !configured {
		if err := EnsureLLM(); err != nil {
			return config.LLM{}, err
		}
		provider, configured, err = config.Load()
	}
	if err != nil {
		return config.LLM{}, err
	}
	if !configured {
		return config.LLM{}, fmt.Errorf("no LLM inference provider configured")
	}
	return provider, nil
}

func promptTask() (string, error) {
	if !terminalInput() {
		return "", fmt.Errorf("a task is required\n\nUsage: yolocoder [--llm-from-env-vars] <task>")
	}
	fmt.Print("What should I change?\n> ")
	task, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read task: %w", err)
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return "", fmt.Errorf("task is required")
	}
	return task, nil
}
