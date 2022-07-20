package persistence

import (
	"context"
	"encoding/json"

	"github.com/georgysavva/scany/pgxscan"
	"github.com/google/uuid"
	nomad "github.com/hashicorp/nomad/api"

	"github.com/input-output-hk/cicero/src/config"
	"github.com/input-output-hk/cicero/src/domain"
	"github.com/input-output-hk/cicero/src/domain/repository"
)

type nomadEventRepository struct {
	DB config.PgxIface
}

func NewNomadEventRepository(db config.PgxIface) repository.NomadEventRepository {
	return nomadEventRepository{db}
}

func (n nomadEventRepository) WithQuerier(querier config.PgxIface) repository.NomadEventRepository {
	return nomadEventRepository{querier}
}

func (n nomadEventRepository) Save(event *domain.NomadEvent) error {
	filterKeys := event.FilterKeys
	if filterKeys == nil {
		filterKeys = []string{}
	}
	return n.DB.QueryRow(
		context.Background(),
		`INSERT INTO nomad_event (topic, "type", "key", filter_keys, "index", payload)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (uid) DO UPDATE
			-- just for RETURNING to work, would otherwise DO NOTHING
			SET topic = EXCLUDED.topic
		RETURNING uid, handled`,
		event.Topic, event.Type, event.Key, filterKeys, event.Index, event.Payload,
	).Scan(&event.Uid, &event.Handled)
}

func (n nomadEventRepository) Update(event *domain.NomadEvent) (err error) {
	_, err = n.DB.Exec(
		context.Background(),
		`UPDATE nomad_event SET handled = $2 WHERE uid = $1`,
		event.Uid[:], event.Handled,
	)
	return
}

func (n nomadEventRepository) GetByHandled(handled bool) (events []domain.NomadEvent, err error) {
	events = []domain.NomadEvent{}
	err = pgxscan.Select(
		context.Background(),
		n.DB, &events,
		`SELECT * FROM nomad_event WHERE handled = $1`,
		handled,
	)
	return
}

func (n nomadEventRepository) GetLastNomadEventIndex() (index uint64, err error) {
	err = pgxscan.Get(
		context.Background(), n.DB, &index,
		`SELECT COALESCE(MAX("index"), 0) FROM nomad_event`,
	)
	return
}

func (n nomadEventRepository) getEventAllocationByJobId(id uuid.UUID, extraWhere string) ([]nomad.Allocation, error) {
	var rows []map[string]interface{}
	if err := pgxscan.Select(context.Background(), n.DB, &rows, `
		SELECT
			payload#>>'{Allocation,CreateTime}' AS create_time,
			payload->>'Allocation' AS alloc
		FROM nomad_event
		WHERE payload#>>'{Allocation,JobID}' = $1
			AND topic = 'Allocation'
			AND type = 'AllocationUpdated'
			`+extraWhere+`
		ORDER BY create_time ASC
	`, id); err != nil {
		return nil, err
	}

	results := make([]nomad.Allocation, len(rows))
	for i, row := range rows {
		if err := json.Unmarshal([]byte(row["alloc"].(string)), &results[i]); err != nil {
			return nil, err
		}
	}

	return results, nil
}

func (n nomadEventRepository) GetEventAllocationByJobId(id uuid.UUID) ([]nomad.Allocation, error) {
	return n.getEventAllocationByJobId(id, "")
}

func (n nomadEventRepository) GetLatestEventAllocationByJobId(id uuid.UUID) ([]nomad.Allocation, error) {
	return n.getEventAllocationByJobId(id, `
		AND NOT EXISTS (
			SELECT NULL
			FROM nomad_event e2
			WHERE e2."index" > nomad_event."index"
				AND topic = 'Allocation'
				AND type = 'AllocationUpdated'
				AND payload#>>'{Allocation,ID}' = nomad_event.payload#>>'{Allocation,ID}'
				AND payload#>>'{Allocation,JobID}' = $1
		)
	`)
}
