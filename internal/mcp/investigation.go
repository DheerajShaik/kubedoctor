package mcp

import (
	"context"
	"errors"

	"github.com/DheerajShaik/kubedoctor/internal/bundle"
	"github.com/DheerajShaik/kubedoctor/internal/investigation"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

type evidenceCollector interface {
	Capture(context.Context, model.ScanRequest) model.Snapshot
}

type artifactArguments struct {
	ArtifactID string `json:"artifact_id" jsonschema:"Opaque artifact ID from this server session"`
}

type comparisonArguments struct {
	BeforeID string `json:"before_id" jsonschema:"Opaque ID of the earlier artifact"`
	AfterID  string `json:"after_id" jsonschema:"Opaque ID of the later artifact"`
}

type relationshipResult struct {
	SchemaVersion string                       `json:"schema_version"`
	Scope         model.Scope                  `json:"scope"`
	Relationships []investigation.Relationship `json:"relationships"`
	Limitations   []string                     `json:"limitations"`
	Truncated     bool                         `json:"truncated"`
}

type historicalResult struct {
	Scope   model.Scope          `json:"scope"`
	Status  model.HistoryStatus  `json:"status"`
	History *model.HistoryReport `json:"history,omitempty"`
}

func (s *sessionState) addInvestigationTools(server *protocol.Server) {
	addTool(server, captureNamespace, "Capture authorized namespace evidence into this server session", func(ctx context.Context, arguments namespaceArguments) (artifactReference, error) {
		return s.capture(ctx, model.NamespaceScope, arguments.Namespace)
	})
	addTool(server, captureCluster, "Capture cluster evidence when enabled at startup", func(ctx context.Context, _ struct{}) (artifactReference, error) {
		return s.capture(ctx, model.ClusterScope, "")
	})
	addTool(server, listArtifacts, "List authorized session artifacts", func(context.Context, struct{}) (artifactList, error) {
		return s.artifacts.list(), nil
	})
	addTool(server, inspectBundle, "Inspect the inventory and coverage of an authorized evidence bundle",
		artifactOperation(s, func(evidence *bundle.Bundle) (bundle.Inventory, error) {
			return evidence.Inventory(), nil
		}))
	addTool(server, analyzeBundle, "Replay the investigation from an authorized evidence bundle",
		artifactOperation(s, replayInvestigation))
	addTool(server, getEvidence, "Retrieve individual findings, evidence and coverage from a replayed bundle",
		artifactOperation(s, func(evidence *bundle.Bundle) (model.Report, error) {
			analysis, err := evidence.Analyze(bundle.Replay)
			if err != nil {
				return model.Report{}, errors.New("Bundle replay requires a compatible analyzer version")
			}
			return analysis.Report, nil
		}))
	addTool(server, getRelationships, "Retrieve evidence relationships and limits from an authorized bundle",
		artifactOperation(s, func(evidence *bundle.Bundle) (relationshipResult, error) {
			view, err := replayInvestigation(evidence)
			if err != nil {
				return relationshipResult{}, err
			}
			return relationshipResult{
				SchemaVersion: view.SchemaVersion, Scope: view.Scope, Relationships: view.Relationships,
				Limitations: view.Limitations, Truncated: view.Truncated,
			}, nil
		}))
	addTool(server, compareCaptures, "Compare two authorized captures with compatible scope, provenance and rules", s.compare)
	addTool(server, historicalTool, "Retrieve historical resource findings contained in an authorized bundle",
		artifactOperation(s, func(evidence *bundle.Bundle) (historicalResult, error) {
			view, err := replayInvestigation(evidence)
			if err != nil {
				return historicalResult{}, err
			}
			result := historicalResult{Scope: view.Scope, Status: model.HistoryUnavailable, History: view.History}
			if result.History != nil {
				result.Status = result.History.Status
			}
			return result, nil
		}))
}

func (s *sessionState) capture(ctx context.Context, kind model.ScopeKind, namespace string) (artifactReference, error) {
	scope, err := s.scope(kind, namespace)
	if err != nil {
		return artifactReference{}, err
	}
	capturer, ok := s.scanner.(evidenceCollector)
	if !ok {
		return artifactReference{}, errors.New("Evidence capture is unavailable")
	}
	return runOperation(s, ctx, func(ctx context.Context) (artifactReference, error) {
		if !s.artifacts.hasRoom() {
			return artifactReference{}, errArtifactLimit
		}
		snapshot := capturer.Capture(ctx, model.ScanRequest{Scope: scope})
		if ctx.Err() != nil {
			return artifactReference{}, errOperationTimeout
		}
		if snapshot.Scope != scope {
			return artifactReference{}, errArtifactScope
		}
		if s.options.History != nil {
			s.options.History.Enrich(ctx, &snapshot)
		}
		if ctx.Err() != nil {
			return artifactReference{}, errOperationTimeout
		}
		evidence, err := bundle.New(snapshot, s.options.Version)
		if err != nil {
			return artifactReference{}, errors.New("Could not create a verified MCP evidence bundle")
		}
		return s.artifacts.register(ctx, evidence)
	})
}

func artifactOperation[T any](state *sessionState, handle func(*bundle.Bundle) (T, error)) func(context.Context, artifactArguments) (T, error) {
	return func(ctx context.Context, args artifactArguments) (T, error) {
		return runOperation(state, ctx, func(ctx context.Context) (T, error) {
			var zero T
			evidence, err := state.artifacts.load(ctx, args.ArtifactID)
			if err != nil {
				return zero, err
			}
			result, err := handle(evidence)
			if ctx.Err() != nil {
				return zero, errOperationTimeout
			}
			return result, err
		})
	}
}

func replayInvestigation(evidence *bundle.Bundle) (investigation.Investigation, error) {
	view, err := investigation.FromBundle(evidence, bundle.Replay)
	if err != nil {
		return investigation.Investigation{}, errors.New("Bundle replay requires a compatible analyzer version")
	}
	return view, nil
}

func (s *sessionState) compare(ctx context.Context, args comparisonArguments) (investigation.Comparison, error) {
	return runOperation(s, ctx, func(ctx context.Context) (investigation.Comparison, error) {
		ids := [2]string{args.BeforeID, args.AfterID}
		for _, id := range ids {
			if !s.artifacts.permits(id) {
				return investigation.Comparison{}, errArtifactMissing
			}
		}
		var views [2]investigation.Investigation
		for i, id := range ids {
			evidence, err := s.artifacts.load(ctx, id)
			if err != nil {
				return investigation.Comparison{}, err
			}
			views[i], err = replayInvestigation(evidence)
			if err != nil {
				return investigation.Comparison{}, err
			}
			if ctx.Err() != nil {
				return investigation.Comparison{}, errOperationTimeout
			}
		}
		comparison := investigation.Compare(views[0], views[1])
		if ctx.Err() != nil {
			return investigation.Comparison{}, errOperationTimeout
		}
		return comparison, nil
	})
}
