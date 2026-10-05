package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/modellife"
	"github.com/toasterbook88/axis/internal/models"
)

func runDailyModelStart(ctx context.Context, cmd *cobra.Command, modelName string) error {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return fmt.Errorf("model name is required")
	}
	live, _ := cmd.Flags().GetBool("live")
	cacheAddr, _ := cmd.Flags().GetString("cache-addr")
	if cacheAddr == "" {
		cacheAddr = api.DefaultAddr()
	}
	format, _ := cmd.Flags().GetString("format")
	if format == "" {
		format = "text"
	}
	snap, source, err := loadModelCommandSnapshot(ctx, live, cacheAddr, "start", false)
	if err != nil {
		return err
	}
	pick, err := modellife.PickServingNode(snap.Nodes, modelName)
	if err != nil {
		return err
	}
	if pick.Already {
		return writeDailyPick(cmd, pick, snap, source, format)
	}
	if pick.Runtime != models.EngineOllama {
		return fmt.Errorf("node %s runtime %s is not a load target for %s", pick.Node, pick.Runtime, modelName)
	}
	if format == "text" {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), pick.Sentence()); err != nil {
			return err
		}
	}
	nf, cfgNode, err := resolveModelNodeFromSnapshot(snap, pick.Node)
	if err != nil {
		return err
	}
	profile := models.ModelRunProfile{
		Schema:       models.ModelRunSchema,
		Node:         pick.Node,
		Engine:       models.EngineOllama,
		ArtifactKind: models.ArtifactOllamaModelName,
		OllamaModel:  modelName,
		BindHost:     "127.0.0.1",
	}
	if snap.Publication != nil {
		profile.SnapshotPublicationID = snap.Publication.ID
	}
	return placeOllamaModel(ctx, cmd, nf, cfgNode, profile, source, snap, time.Now().UTC(), format)
}

func writeDailyPick(cmd *cobra.Command, pick modellife.ServingPick, snap *models.ClusterSnapshot, source, format string) error {
	if format == "text" || format == "" {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), pick.Sentence())
		return err
	}
	now := time.Now().UTC()
	receipt := models.ModelOperationReceipt{
		Schema:         "axis.model-operation/v1",
		ID:             models.GenerateID("mo"),
		Action:         models.ModelOperationStart,
		Status:         models.ModelOperationCompleted,
		Disposition:    "already_serving",
		Node:           pick.Node,
		Engine:         pick.Runtime,
		Model:          pick.Model,
		SnapshotSource: source,
		StartedAt:      now,
		CompletedAt:    now,
	}
	if snap != nil {
		receipt.SnapshotAt = snap.Timestamp
		if snap.Publication != nil {
			receipt.PublicationID = snap.Publication.ID
		}
	}
	return printOutput(cmd.OutOrStdout(), receipt, format)
}
