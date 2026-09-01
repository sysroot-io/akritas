package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	akritasinstructions "akritas/internal/instructions"
)

func runChangeSimulationCLI(arguments []string) {
	flags := flag.NewFlagSet("simulate-change", flag.ExitOnError)
	baseURL := flags.String("base-url", "http://127.0.0.1:8080/v1", "OpenAI-compatible API base URL ending in /v1")
	model := flags.String("model", "", "model ID; empty discovers the first /v1/models entry")
	apiKeyEnvironment := flags.String("api-key-env", "OPENAI_API_KEY", "environment variable containing the API key; empty disables authentication")
	systemInstructionsPath := flags.String("system-instructions", akritasinstructions.DefaultPath, "UTF-8 Markdown file with global model instructions")
	responseLanguage := flags.String("response-language", defaultResponseLanguage, "BCP 47 language tag for model-generated prose")
	root := flags.String("root", "", "snapshot root directory")
	requestPath := flags.String("request", "", "change request file relative to root")
	var filePaths repeatedStringFlag
	flags.Var(&filePaths, "file", "repository file relative to root; flag may be repeated")
	maxTokens := flags.Int("max-tokens", 1600, "maximum response tokens")
	temperature := flags.Float64("temperature", 0, "model sampling temperature")
	requestTimeout := flags.Duration("request-timeout", 10*time.Minute, "OpenAI HTTP request timeout")
	printPrompt := flags.Bool("print-prompt", false, "print the exact system and user prompts without calling the model")
	_ = flags.Parse(arguments)
	if *maxTokens <= 0 || *temperature < 0 || *requestTimeout <= 0 {
		panic("simulate-change requires valid token, temperature and timeout limits")
	}
	normalizedResponseLanguage, err := normalizeResponseLanguage(*responseLanguage)
	if err != nil {
		panic(err)
	}
	systemInstructions, err := akritasinstructions.Load(*systemInstructionsPath)
	if err != nil {
		panic(err)
	}

	if *printPrompt && len(filePaths) == 0 {
		panic("simulate-change -print-prompt requires at least one -file; automatic discovery calls the model")
	}
	if *printPrompt {
		snapshot, err := loadChangeSimulationSnapshot(*root, *requestPath, filePaths)
		if err != nil {
			panic(err)
		}
		userPrompt, err := buildChangeSimulationPrompt(snapshot)
		if err != nil {
			panic(err)
		}
		systemPrompt, err := changeSimulationSystemPromptForLanguage(normalizedResponseLanguage)
		if err != nil {
			panic(err)
		}
		systemPrompt = systemInstructions + "\n\n" + systemPrompt
		fmt.Printf(
			"SYSTEM\n%s\n\nUSER\n%s\n\nTOOL %s\n%s\n\nTOOL_CHOICE\nrequired\n",
			systemPrompt, userPrompt,
			changeSimulationProposalToolName, changeSimulationProposalSchema,
		)
		return
	}

	apiKey := ""
	if strings.TrimSpace(*apiKeyEnvironment) != "" {
		apiKey = os.Getenv(*apiKeyEnvironment)
	}
	client, err := newOpenAIToolClient(
		*baseURL, *model, apiKey,
		&http.Client{Timeout: *requestTimeout},
	)
	if err != nil {
		panic(err)
	}
	if err := client.SetSystemInstructions(systemInstructions); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *requestTimeout)
	defer cancel()
	if err := client.DiscoverModel(ctx); err != nil {
		panic(err)
	}
	var snapshot changeSimulationSnapshot
	if len(filePaths) == 0 {
		rootPath, normalizedRequestPath, request, err := loadChangeRequest(*root, *requestPath)
		if err != nil {
			panic(fmt.Errorf("load change request: %w", err))
		}
		snapshot, err = discoverChangeSimulationSnapshot(
			ctx, client, rootPath, normalizedRequestPath, request, *maxTokens, *temperature,
		)
		if err != nil {
			panic(err)
		}
	} else {
		snapshot, err = loadChangeSimulationSnapshot(*root, *requestPath, filePaths)
		if err != nil {
			panic(err)
		}
	}
	result, err := runChangeSimulationWithLanguage(
		ctx, client, snapshot, *maxTokens, *temperature, normalizedResponseLanguage,
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(formatChangeSimulationResult(result))
}
