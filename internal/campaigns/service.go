package campaigns

import (
	"context"
	"io"
	"io/ioutil"

	"github.com/pkg/errors"
	"github.com/sourcegraph/src-cli/internal/api"
)

type Service struct {
	*ActionLogger
	ServiceExecutorOpts
}

type ServiceExecutorOpts struct {
	ExecutorOpts
	Parallelism int
}

type ServiceOpts struct {
	ServiceExecutorOpts

	Client        api.Client
	VerboseLogger bool
	KeepLogs      bool
}

var (
	ErrMalformedOnQueryOrRepository = errors.New("malformed on field; missing either a repository name or a query")
)

func NewService(opts *ServiceOpts) *Service {
	logger := NewActionLogger(opts.VerboseLogger, opts.KeepLogs)

	return &Service{
		ActionLogger:        logger,
		ServiceExecutorOpts: opts.ServiceExecutorOpts,
	}
}

func (svc *Service) ParseCampaignSpec(in io.Reader) (*CampaignSpec, error) {
	data, err := ioutil.ReadAll(in)
	if err != nil {
		return nil, errors.Wrap(err, "reading campaign spec")
	}

	spec, err := ParseCampaignSpec(data)
	if err != nil {
		return nil, errors.Wrap(err, "parsing campaign spec")
	}
	return spec, nil
}
