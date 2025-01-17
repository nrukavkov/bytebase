package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/pkg/errors"

	"github.com/bytebase/bytebase/backend/common"
)

// PipelineMessage is the message for pipelines.
type PipelineMessage struct {
	ProjectID string
	Name      string
	Stages    []*StageMessage
	// Output only.
	ID         int
	CreatorUID int
	CreatedTs  int64
	UpdaterUID int
	UpdatedTs  int64
}

// PipelineFind is the API message for finding pipelines.
type PipelineFind struct {
	ID        *int
	ProjectID *string

	Limit  *int
	Offset *int
}

// CreatePipelineV2 creates a pipeline.
func (s *Store) CreatePipelineV2(ctx context.Context, create *PipelineMessage, creatorID int) (*PipelineMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	query := `
		INSERT INTO pipeline (
			project_id,
			creator_id,
			updater_id,
			name
		)
		VALUES (
			(SELECT project.id FROM project WHERE project.resource_id = $1),
			$2,
			$3,
			$4
		)
		RETURNING id, created_ts
	`
	pipeline := &PipelineMessage{
		ProjectID:  create.ProjectID,
		CreatorUID: creatorID,
		UpdaterUID: creatorID,
		Name:       create.Name,
	}
	if err := tx.QueryRowContext(ctx, query,
		create.ProjectID,
		creatorID,
		creatorID,
		create.Name,
	).Scan(
		&pipeline.ID,
		&pipeline.CreatedTs,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, common.FormatDBErrorEmptyRowWithQuery(query)
		}
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	pipeline.UpdatedTs = pipeline.CreatedTs
	s.pipelineCache.Add(pipeline.ID, pipeline)
	return pipeline, nil
}

// GetPipelineV2ByID gets the pipeline by ID.
func (s *Store) GetPipelineV2ByID(ctx context.Context, id int) (*PipelineMessage, error) {
	if v, ok := s.pipelineCache.Get(id); ok {
		return v, nil
	}
	pipelines, err := s.ListPipelineV2(ctx, &PipelineFind{ID: &id})
	if err != nil {
		return nil, err
	}

	if len(pipelines) == 0 {
		return nil, nil
	} else if len(pipelines) > 1 {
		return nil, &common.Error{Code: common.Conflict, Err: errors.Errorf("found %d pipelines, expect 1", len(pipelines))}
	}
	pipeline := pipelines[0]
	return pipeline, nil
}

// ListPipelineV2 lists pipelines.
func (s *Store) ListPipelineV2(ctx context.Context, find *PipelineFind) ([]*PipelineMessage, error) {
	where, args := []string{"TRUE"}, []any{}
	if v := find.ID; v != nil {
		where, args = append(where, fmt.Sprintf("pipeline.id = $%d", len(args)+1)), append(args, *v)
	}
	if v := find.ProjectID; v != nil {
		where, args = append(where, fmt.Sprintf("project.resource_id = $%d", len(args)+1)), append(args, *v)
	}
	query := fmt.Sprintf(`
		SELECT
			pipeline.id,
			pipeline.creator_id,
			pipeline.created_ts,
			pipeline.updater_id,
			pipeline.updated_ts,
			project.resource_id,
			pipeline.name
		FROM pipeline
		LEFT JOIN project ON pipeline.project_id = project.id
		WHERE %s
		ORDER BY id DESC`, strings.Join(where, " AND "))
	if v := find.Limit; v != nil {
		query += fmt.Sprintf(" LIMIT %d", *v)
	}
	if v := find.Offset; v != nil {
		query += fmt.Sprintf(" OFFSET %d", *v)
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pipelines []*PipelineMessage
	for rows.Next() {
		var pipeline PipelineMessage
		if err := rows.Scan(
			&pipeline.ID,
			&pipeline.CreatorUID,
			&pipeline.CreatedTs,
			&pipeline.UpdaterUID,
			&pipeline.UpdatedTs,
			&pipeline.ProjectID,
			&pipeline.Name,
		); err != nil {
			return nil, err
		}
		pipelines = append(pipelines, &pipeline)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	for _, pipeline := range pipelines {
		s.pipelineCache.Add(pipeline.ID, pipeline)
	}
	return pipelines, nil
}
