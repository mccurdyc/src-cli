package campaigns

import (
	"context"
	"encoding/json"
	"io"
	"io/ioutil"

	"github.com/pkg/errors"
	"github.com/sourcegraph/src-cli/internal/api"
)

type Service struct {
	*ActionLogger
	ServiceExecutorOpts

	client api.Client
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
		client:              opts.Client,
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

func (svc *Service) ResolveRepositories(ctx context.Context, on *OnQueryOrRepository) ([]*Repository, error) {
	if on.RepositoriesMatchingQuery != "" {
		return svc.resolveRepositorySearch(ctx, on.RepositoriesMatchingQuery)
	} else if on.Repository != "" {
		repo, err := svc.resolveRepositoryName(ctx, on.Repository)
		if err != nil {
			return nil, err
		}
		return []*Repository{repo}, nil
	}

	// This shouldn't happen on any campaign spec that has passed validation,
	// but, alas, software.
	return nil, ErrMalformedOnQueryOrRepository
}

const repositoryNameQuery = `
query Repository(
    $name: String!,
) {
    repository(
        name: $name
    ) {
        ...repositoryFields
    }
}
` + repositoryFieldsFragment

func (svc *Service) resolveRepositoryName(ctx context.Context, name string) (*Repository, error) {
	var result struct{ Repository Repository }
	if ok, err := svc.client.NewRequest(repositoryNameQuery, map[string]interface{}{
		"name": name,
	}).Do(ctx, &result); err != nil || !ok {
		return nil, err
	}
	return &result.Repository, nil
}

// TODO: search result alerts.
const repositorySearchQuery = `
query ChangesetRepos(
    $query: String!,
) {
    search(query: $query, version: V2) {
        results {
            results {
                __typename
                ... on Repository {
                    ...repositoryFields
                }
                ... on FileMatch {
                    repository {
                        ...repositoryFields
                    }
                }
            }
        }
    }
}
` + repositoryFieldsFragment

func (svc *Service) resolveRepositorySearch(ctx context.Context, query string) ([]*Repository, error) {
	var result struct {
		Search struct {
			Results struct {
				Results []searchResult
			}
		}
	}
	if ok, err := svc.client.NewRequest(repositorySearchQuery, map[string]interface{}{
		"query": query,
	}).Do(ctx, &result); err != nil || !ok {
		return nil, err
	}

	var repos []*Repository
	for _, r := range result.Search.Results.Results {
		repos = append(repos, &r.Repository)
	}
	return repos, nil
}

type searchResult struct {
	Repository
}

func (sr *searchResult) UnmarshalJSON(data []byte) error {
	var tn struct {
		Typename string `json:"__typename"`
	}
	if err := json.Unmarshal(data, &tn); err != nil {
		return err
	}

	switch tn.Typename {
	case "FileMatch":
		var result struct{ Repository Repository }
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}

		sr.Repository = result.Repository
		return nil

	case "Repository":
		return json.Unmarshal(data, &sr.Repository)

	default:
		return errors.Errorf("unknown GraphQL type %q", tn.Typename)
	}
}
