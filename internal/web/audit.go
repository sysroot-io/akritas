package web

import (
	"log"

	"akritas/internal/audit"
)

type opsAuditRun struct {
	store    *audit.Store
	runID    string
	finished bool
}

func (server *opsServer) beginAuditRun(source, workspace string, metadata map[string]string) *opsAuditRun {
	handle := &opsAuditRun{store: server.auditStore}
	if server.auditStore == nil {
		return handle
	}
	actor := "anonymous"
	if server.apiKey != "" {
		actor = "api-key"
	}
	run, err := server.auditStore.StartRun(source, actor, workspace, server.modelID, metadata)
	if err != nil {
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
		log.Printf("level=error component=akritas audit=event run_id=%q error=%q", run.runID, err)
	}
}

func (run *opsAuditRun) addToolEvents(result openAIToolLoopResult) {
	for index, call := range result.Calls {
		status := "succeeded"
		metadata := map[string]string{"tool": call.Name}
		if index < len(result.Results) && result.Results[index].Error != nil {
			status = "failed"
			metadata["error_code"] = result.Results[index].Error.Code
		}
		run.addEvent("tool_call", status, metadata)
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
		log.Printf("level=error component=akritas audit=finish run_id=%q error=%q", run.runID, err)
		return
	}
	run.finished = true
}
