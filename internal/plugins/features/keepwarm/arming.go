package keepwarm

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

func hasFinishedOSCommand(events []lipapi.ToolEvent) bool {
	for _, event := range events {
		if event.Kind == lipapi.ToolEventFinished && event.Category == lipapi.ToolCategoryOSCommand {
			return true
		}
	}
	return false
}
