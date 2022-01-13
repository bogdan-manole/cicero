package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/grafana/loki/pkg/loghttp"
	nomad "github.com/hashicorp/nomad/api"
	"github.com/jackc/pgx/v4"
	"github.com/pkg/errors"
	prometheus "github.com/prometheus/client_golang/api"
	"github.com/rs/zerolog"

	"github.com/input-output-hk/cicero/src/application"
	"github.com/input-output-hk/cicero/src/config"
	"github.com/input-output-hk/cicero/src/domain"
	"github.com/input-output-hk/cicero/src/domain/repository"
	"github.com/input-output-hk/cicero/src/infrastructure/persistence"
)

type RunService interface {
	GetByNomadJobId(uuid.UUID) (domain.Run, error)
	GetOutputByNomadJobId(uuid.UUID) (domain.RunOutput, error)
	GetByActionId(uuid.UUID) ([]*domain.Run, error)
	GetAll() ([]*domain.Run, error)
	Save(pgx.Tx, *domain.Run, *domain.RunOutput) error
	Update(pgx.Tx, *domain.Run) error
	End(pgx.Tx, *domain.Run) error
	Cancel(*domain.Run) error
	JobLogs(uuid.UUID) (*domain.LokiOutput, error)
	RunLogs(allocId string, taskGroup string) (*domain.LokiOutput, error)
}

type runService struct {
	logger              zerolog.Logger
	runRepository       repository.RunRepository
	runOutputRepository repository.RunOutputRepository
	prometheus          prometheus.Client
	nomadClient         application.NomadClient
	db                  config.PgxIface
}

func NewRunService(db config.PgxIface, prometheusAddr string, nomadClient application.NomadClient, logger *zerolog.Logger) RunService {
	impl := runService{
		logger:              logger.With().Str("component", "RunService").Logger(),
		runRepository:       persistence.NewRunRepository(db),
		runOutputRepository: persistence.NewRunOutputRepository(db),
		nomadClient:         nomadClient,
		db:                  db,
	}

	if prom, err := prometheus.NewClient(prometheus.Config{
		Address: prometheusAddr,
	}); err != nil {
		impl.logger.Fatal().Err(err).Msg("Failed to create new prometheus client")
		return nil
	} else {
		impl.prometheus = prom
	}

	return &impl
}

func (self *runService) GetByNomadJobId(id uuid.UUID) (run domain.Run, err error) {
	self.logger.Debug().Str("nomad-job-id", id.String()).Msg("Getting Run by Nomad Job ID")
	run, err = self.runRepository.GetByNomadJobId(id)
	if err != nil {
		err = errors.WithMessagef(err, "Could not select existing Run by Nomad Job ID %q", id)
	}
	return
}

func (self *runService) GetOutputByNomadJobId(id uuid.UUID) (output domain.RunOutput, err error) {
	self.logger.Debug().Str("nomad-job-id", id.String()).Msg("Getting Run Output by Nomad Job ID")
	output, err = self.runOutputRepository.GetByRunId(id)
	if err != nil {
		err = errors.WithMessagef(err, "Could not select existing Run Output by Nomad Job ID %q", id)
	}
	return
}

func (self *runService) GetByActionId(id uuid.UUID) (runs []*domain.Run, err error) {
	self.logger.Debug().Str("action-id", id.String()).Msg("Getting Run by Action ID")
	runs, err = self.runRepository.GetByActionId(id)
	if err != nil {
		err = errors.WithMessagef(err, "Could not select existing Run by Action ID %q", id)
	}
	return
}

func (self *runService) GetAll() ([]*domain.Run, error) {
	self.logger.Debug().Msg("Getting all Runs")
	return self.runRepository.GetAll()
}

func (self *runService) Save(tx pgx.Tx, run *domain.Run, output *domain.RunOutput) error {
	self.logger.Debug().Msg("Saving new Run")
	if err := self.runRepository.Save(tx, run); err != nil {
		return errors.WithMessagef(err, "Could not insert Run")
	}
	if err := self.runOutputRepository.Save(tx, run.NomadJobID, output); err != nil {
		return errors.WithMessagef(err, "Could not insert Run Output")
	}
	self.logger.Debug().Str("id", run.NomadJobID.String()).Msg("Created Run")
	return nil
}

func (self *runService) Update(tx pgx.Tx, run *domain.Run) error {
	self.logger.Debug().Str("id", run.NomadJobID.String()).Msg("Updating Run")
	if err := self.runRepository.Update(tx, run); err != nil {
		return errors.WithMessagef(err, "Could not update Run with ID %q", run.NomadJobID)
	}
	self.logger.Debug().Str("id", run.NomadJobID.String()).Msg("Updated Run")
	return nil
}

func (self *runService) End(tx pgx.Tx, run *domain.Run) error {
	self.logger.Debug().Str("id", run.NomadJobID.String()).Msg("Ending Run")
	if err := self.runRepository.Update(tx, run); err != nil {
		return errors.WithMessagef(err, "Could not update Run with ID %q", run.NomadJobID)
	}
	if err := self.runOutputRepository.Delete(tx, run.NomadJobID); err != nil {
		return errors.WithMessagef(err, "Could not update Run Output with ID %q", run.NomadJobID)
	}
	self.logger.Debug().Str("id", run.NomadJobID.String()).Msg("Ended Run")
	return nil
}

func (self *runService) Cancel(run *domain.Run) error {
	self.logger.Debug().Str("id", run.NomadJobID.String()).Msg("Stopping Run")
	// Nomad does not know whether the job simply ran to finish
	// or was stopped manually. Delete output to avoid publishing them.
	if err := self.db.BeginFunc(context.Background(), func(tx pgx.Tx) error {
		if err := self.runOutputRepository.Delete(tx, run.NomadJobID); err != nil {
			return err
		} else if _, _, err := self.nomadClient.JobsDeregister(run.NomadJobID.String(), false, &nomad.WriteOptions{}); err != nil {
			return errors.WithMessagef(err, "Failed to deregister job %q", run.NomadJobID)
		}
		return nil
	}); err != nil {
		return err
	}
	self.logger.Debug().Str("id", run.NomadJobID.String()).Msg("Stopped Run")
	return nil
}

func (self *runService) JobLogs(nomadJobID uuid.UUID) (*domain.LokiOutput, error) {
	return self.LokiQueryRange(fmt.Sprintf(
		`{nomad_job_id=%q}`,
		nomadJobID.String(),
	))
}

func (self *runService) RunLogs(allocID, taskGroup string) (*domain.LokiOutput, error) {
	return self.LokiQueryRange(fmt.Sprintf(
		`{nomad_alloc_id=%q,nomad_task_group=%q}`,
		allocID,
		taskGroup,
	))
}

func (self *runService) LokiQueryRange(query string) (*domain.LokiOutput, error) {
	linesToFetch := 10000
	// TODO: figure out the correct value for our infra, 5000 is the default
	// configuration in loki
	var limit int64 = 5000
	from := time.Unix(0, 0)
	output := &domain.LokiOutput{
		Stdout: []domain.LokiLine{},
		Stderr: []domain.LokiLine{},
	}

	// TODO: reduce allocations in this loop
	for {
		req, err := http.NewRequest(
			"GET",
			self.prometheus.URL("/loki/api/v1/query_range", nil).String(),
			http.NoBody,
		)
		if err != nil {
			return output, err
		}

		q := req.URL.Query()
		q.Set("query", query)
		q.Set("limit", strconv.FormatInt(limit, 10))
		q.Set("start", strconv.FormatInt(from.UnixNano(), 10))
		q.Set("end", strconv.FormatInt(time.Now().UnixNano(), 10))
		q.Set("direction", "FORWARD")
		req.URL.RawQuery = q.Encode()

		done, body, err := self.prometheus.Do(context.Background(), req)
		if err != nil {
			return output, errors.WithMessage(err, "Failed to talk with loki")
		}

		if done.StatusCode/100 != 2 {
			return output, fmt.Errorf("Error response %d from Loki: %s", done.StatusCode, string(body))
		}

		response := loghttp.QueryResponse{}

		err = json.Unmarshal(body, &response)
		if err != nil {
			return output, err
		}

		streams, ok := response.Data.Result.(loghttp.Streams)
		if !ok {
			return output, fmt.Errorf("Unexpected loki result type: %s", response.Data.Result.Type())
		}

		if len(streams) == 0 {
			return output, nil
		}

		for _, stream := range streams {
			source, ok := stream.Labels.Map()["source"]

			for _, entry := range stream.Entries {
				if ok && source == "stderr" {
					output.Stderr = append(output.Stderr, domain.LokiLine{Time: entry.Timestamp, Text: entry.Line})
				} else {
					output.Stdout = append(output.Stdout, domain.LokiLine{Time: entry.Timestamp, Text: entry.Line})
				}

				if (len(output.Stdout) + len(output.Stderr)) >= linesToFetch {
					return output, nil
				}
			}

			if int64(len(stream.Entries)) >= limit {
				from = stream.Entries[len(stream.Entries)-1].Timestamp
			} else if int64(len(stream.Entries)) < limit {
				return output, nil
			}
		}
	}
}
