package service

import (
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v4"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"

	"github.com/input-output-hk/cicero/src/config"
	"github.com/input-output-hk/cicero/src/domain"
	"github.com/input-output-hk/cicero/src/domain/repository"
	"github.com/input-output-hk/cicero/src/infrastructure/persistence"
)

type FactService interface {
	GetById(uuid.UUID) (domain.Fact, error)
	GetBinaryById(pgx.Tx, uuid.UUID) (io.ReadSeekCloser, error)
	GetLatestByFields([][]string) (domain.Fact, error)
	GetByFields([][]string) ([]*domain.Fact, error)
	Save(pgx.Tx, *domain.Fact, io.Reader) error
	// TODO sometimes you need a Tx, sometimes not...
	// → SaveTx() and Save() etc? another wrapper? Tx() to get one?
}

type factService struct {
	logger         zerolog.Logger
	factRepository repository.FactRepository
}

func NewFactService(db config.PgxIface, logger *zerolog.Logger) FactService {
	return &factService{
		logger:         logger.With().Str("component", "FactService").Logger(),
		factRepository: persistence.NewFactRepository(db),
	}
}

func (self *factService) GetById(id uuid.UUID) (fact domain.Fact, err error) {
	self.logger.Debug().Str("id", id.String()).Msg("Getting Fact by ID")
	fact, err = self.factRepository.GetById(id)
	if err != nil {
		err = errors.WithMessagef(err, "Could not select existing Fact for ID: %s", id)
	}
	return
}

func (self *factService) GetBinaryById(tx pgx.Tx, id uuid.UUID) (binary io.ReadSeekCloser, err error) {
	self.logger.Debug().Str("id", id.String()).Msg("Getting binary by ID")
	binary, err = self.factRepository.GetBinaryById(tx, id)
	if err != nil {
		err = errors.WithMessagef(err, "Could not select existing Fact for ID: %s", id)
	}
	return
}

func (self *factService) Save(tx pgx.Tx, fact *domain.Fact, binary io.Reader) error {
	self.logger.Debug().Msg("Saving new Fact")
	if err := self.factRepository.Save(tx, fact, binary); err != nil {
		return errors.WithMessagef(err, "Could not insert Fact")
	}
	self.logger.Debug().Str("id", fact.ID.String()).Msg("Created Fact")
	return nil
}

func (self *factService) GetLatestByFields(fields [][]string) (fact domain.Fact, err error) {
	self.logger.Debug().Interface("fields", fields).Msg("Getting latest Facts by fields")
	fact, err = self.factRepository.GetLatestByFields(fields)
	if err != nil {
		err = errors.WithMessagef(err, "Could not select latest Facts by fields %q", fields)
	}
	return
}

func (self *factService) GetByFields(fields [][]string) (facts []*domain.Fact, err error) {
	self.logger.Debug().Interface("fields", fields).Msg("Getting Facts by fields")
	facts, err = self.factRepository.GetByFields(fields)
	if err != nil {
		err = errors.WithMessagef(err, "Could not select Facts by fields %q", fields)
	}
	return
}
