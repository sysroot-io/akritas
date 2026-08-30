package cli

import (
	"fmt"
	"os"
)

func Run(arguments []string) int {
	if len(arguments) < 1 {
		printUsage()
		return 2
	}
	switch arguments[0] {
	case "serve", "serve-ops":
		runOpsServer(arguments[1:])
	case "simulate-change":
		runChangeSimulationCLI(arguments[1:])
	case "import-local":
		runImportLocalCorpus(arguments[1:])
	case "build-corpus":
		runBuildCorpus(arguments[1:])
	case "build-rag-index":
		runBuildRAGIndex(arguments[1:])
	case "search-rag":
		runSearchRAG(arguments[1:])
	case "inspect-rag-index":
		runInspectRAGIndex(arguments[1:])
	case "list-rag-documents":
		runListRAGDocuments(arguments[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", arguments[0])
		printUsage()
		return 2
	}
	return 0
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: akritas <serve|simulate-change|import-local|build-corpus|build-rag-index|search-rag|inspect-rag-index|list-rag-documents> [flags]")
}
