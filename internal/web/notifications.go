package web

import (
	"context"
	"log"
	"strconv"

	"akritas/internal/alerts"
	"akritas/internal/investigation"
	"akritas/internal/notifications"
)

func (server *opsServer) deliverIncidentNotifications(
	ctx context.Context,
	runID string,
	webhook opsAlertmanagerWebhook,
	result openAIToolLoopResult,
	investigationResult investigation.Result,
	activity []opsToolActivity,
	capabilityGaps []opsCapabilityGap,
) []notifications.DeliveryResult {
	event := alerts.Event{
		State: alerts.State(webhook.Status), CorrelationKey: webhook.GroupKey,
		Name:   webhook.CommonLabels["alertname"],
		Entity: alerts.Entity{Kind: "alert-group", ID: webhook.GroupKey},
		Source: alerts.Source{Type: "alertmanager", Name: webhook.Receiver},
	}
	return server.deliverCanonicalIncidentNotifications(ctx, runID, "", event, result, investigationResult, activity, capabilityGaps)
}

func (server *opsServer) deliverCanonicalIncidentNotifications(
	ctx context.Context,
	runID string,
	incidentID string,
	event alerts.Event,
	result openAIToolLoopResult,
	investigationResult investigation.Result,
	activity []opsToolActivity,
	capabilityGaps []opsCapabilityGap,
) []notifications.DeliveryResult {
	if server == nil || server.notifications == nil {
		return nil
	}
	notificationActivity := make([]notifications.ToolActivity, 0, len(activity))
	for _, item := range activity {
		notificationActivity = append(notificationActivity, notifications.ToolActivity{
			Name: item.Name, ID: item.ID, Status: item.Status, DurationMS: item.DurationMS,
		})
	}
	notificationGaps := make([]notifications.CapabilityGap, 0, len(capabilityGaps))
	for _, gap := range capabilityGaps {
		notificationGaps = append(notificationGaps, notifications.CapabilityGap{
			Step: gap.Step, Capability: gap.Capability, Reason: gap.Reason,
		})
	}
	return server.notifications.Deliver(ctx, notifications.Incident{
		RunID: runID, IncidentID: incidentID, Model: server.modelID,
		AlertStatus: string(event.State), AlertName: event.Name, Entity: event.Entity.Kind + ":" + event.Entity.ID,
		Source: event.Source.Type + ":" + event.Source.Name, GroupKey: event.CorrelationKey,
		RunURL: server.runURL(runID), Answer: result.Answer, Skills: result.Skills,
		Activity: notificationActivity, CapabilityGaps: notificationGaps,
		Investigation: investigationResult, Budget: result.Tracker.Snapshot(),
	})
}

func (server *opsServer) runURL(runID string) string {
	if server == nil || server.publicURL == "" || runID == "" {
		return ""
	}
	return server.publicURL + "/runs/" + runID
}

func (run *opsAuditRun) addNotificationEvents(results []notifications.DeliveryResult) {
	for _, result := range results {
		metadata := map[string]string{
			"receiver":    result.Receiver,
			"type":        result.Type,
			"delivery_id": result.DeliveryID,
		}
		if result.StatusCode != 0 {
			metadata["status_code"] = strconv.Itoa(result.StatusCode)
		}
		if result.ErrorCode != "" {
			metadata["error_code"] = result.ErrorCode
		}
		run.addEvent("notification_delivery", result.Status, metadata)
		if result.Status == "failed" {
			log.Printf(
				"level=warn component=akritas_notification receiver=%q type=%q delivery_id=%q status_code=%d error_code=%q",
				result.Receiver, result.Type, result.DeliveryID, result.StatusCode, result.ErrorCode,
			)
		}
	}
}
