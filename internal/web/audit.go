package web

import (
	"encoding/json"
	"log"

	"akritas/internal/audit"
	"akritas/internal/investigation"
)

type opsAuditRun struct {
	store    *audit.Store
	runID    string
	finished bool
	writeErr error
}

func (server *opsServer) beginAuditRun(source, workspace string, metadata map[string]string) *opsAuditRun {
	actor := "anonymous"
	if server.apiKey != "" {
		actor = "api-key"
	}
	return server.beginAuditRunAs(source, actor, workspace, metadata)
}

func (server *opsServer) beginAuditRunAs(source, actor, workspace string, metadata map[string]string) *opsAuditRun {
	handle := &opsAuditRun{store: server.auditStore}
	if server.auditStore == nil {
		return handle
	}
	run, err := server.auditStore.StartRun(source, actor, workspace, server.modelID, metadata)
	if err != nil {
		handle.writeErr = err
		log.Printf("level=error component=akritas audit=start error=%q", err)
		return handle
	}
	handle.runID = run.ID
	return handle
}

func (run *opsAuditRun) id() string {
	if run == nil {
		return ""
	}
	return run.runID
}

func (run *opsAuditRun) addEvent(eventType, status string, metadata map[string]string) {
	if run == nil || run.store == nil || run.runID == "" || run.finished {
		return
	}
	if err := run.store.AddEvent(run.runID, eventType, status, metadata); err != nil {
		run.writeErr = err
		log.Printf("level=error component=akritas audit=event run_id=%q error=%q", run.runID, err)
	}
}

func (run *opsAuditRun) addToolEvents(result openAIToolLoopResult) {
	for index, call := range result.Calls {
		status := "succeeded"
		metadata := map[string]string{"tool": call.Name, "call_id": call.ID}
		var rawResult []byte
		if index < len(result.Results) && result.Results[index].Error != nil {
			status = "failed"
			metadata["error_code"] = result.Results[index].Error.Code
		}
		if index < len(result.Results) {
			rawResult, _ = json.Marshal(result.Results[index])
		}
		if run == nil || run.store == nil || run.runID == "" || run.finished {
			continue
		}
		if err := run.store.AddToolEvent(run.runID, status, metadata, call.Arguments, rawResult); err != nil {
			run.writeErr = err
			log.Printf("level=error component=akritas audit=tool_event run_id=%q error=%q", run.runID, err)
		}
	}
	for _, skill := range result.Skills {
		run.addEvent("skill_selected", "accepted", map[string]string{"skill": skill})
	}
	for index, call := range result.Calls {
		if call.Name != localRAGSearchToolName || index >= len(result.Results) || result.Results[index].Error != nil {
			continue
		}
		var output struct {
			Results []struct {
				DocumentID string `json:"document_id"`
				Title      string `json:"title"`
			} `json:"results"`
		}
		if err := json.Unmarshal(result.Results[index].Output, &output); err != nil {
			continue
		}
		for _, item := range output.Results {
			run.addEvent("context_retrieved", "accepted", map[string]string{
				"document_id": item.DocumentID, "title": item.Title, "call_id": call.ID,
			})
		}
	}
}

func (run *opsAuditRun) setInvestigationResult(result investigation.Result) {
	if run == nil || run.store == nil || run.runID == "" || run.finished {
		return
	}
	if err := run.store.SetInvestigationResult(run.runID, result); err != nil {
		run.writeErr = err
		log.Printf("level=error component=akritas audit=investigation run_id=%q error=%q", run.runID, err)
	}
}

func (run *opsAuditRun) succeed(metadata map[string]string) {
	run.finish(audit.RunSucceeded, "", metadata)
}

func (run *opsAuditRun) failIfRunning(code string) {
	if run == nil || run.finished {
		return
	}
	run.finish(audit.RunFailed, code, nil)
}

func (run *opsAuditRun) finish(status audit.RunStatus, errorCode string, metadata map[string]string) {
	if run == nil || run.store == nil || run.runID == "" || run.finished {
		return
	}
	if err := run.store.FinishRun(run.runID, status, errorCode, metadata); err != nil {
		run.writeErr = err
		log.Printf("level=error component=akritas audit=finish run_id=%q error=%q", run.runID, err)
		return
	}
	run.finished = true
}

func (run *opsAuditRun) error() error {
	if run == nil {
		return nil
	}
	return run.writeErr
}
