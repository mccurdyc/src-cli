package campaigns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"github.com/sourcegraph/src-cli/internal/api"
)

type Service struct {
	client api.Client
}

type ServiceOpts struct {
	Client api.Client
}

var (
	ErrMalformedOnQueryOrRepository = errors.New("malformed on field; missing either a repository name or a query")
)

func NewService(opts *ServiceOpts) *Service {
	return &Service{
		client: opts.Client,
	}
}

type CampaignSpecID string
type ChangesetSpecID string

const createCampaignSpecMutation = `
mutation CreateCampaignSpec(
    $namespace: ID!,
    $spec: String!,
    $changesetSpecs: [ID!]!
) {
    createCampaignSpec(
        namespace: $namespace, 
        campaignSpec: $spec,
        changesetSpecs: $changesetSpecs
    ) {
        id
        applyURL
    }
}
`

func (svc *Service) CreateCampaignSpec(ctx context.Context, namespace string, spec *CampaignSpec, ids []ChangesetSpecID) (CampaignSpecID, string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", "", errors.Wrap(err, "marshalling campaign spec JSON")
	}

	var result struct {
		CreateCampaignSpec struct {
			ID       string
			ApplyURL string
		}
	}
	if ok, err := svc.client.NewRequest(createCampaignSpecMutation, map[string]interface{}{
		"namespace":      namespace,
		"spec":           string(raw),
		"changesetSpecs": ids,
	}).Do(ctx, &result); err != nil || !ok {
		return "", "", err
	}

	return CampaignSpecID(result.CreateCampaignSpec.ID), result.CreateCampaignSpec.ApplyURL, nil

}

const createChangesetSpecMutation = `
mutation CreateChangesetSpec($spec: String!) {
    createChangesetSpec(changesetSpec: $spec) {
        ... on HiddenChangesetSpec {
            id
        }
        ... on VisibleChangesetSpec {
            id
        }
    }
}
`

func (svc *Service) CreateChangesetSpec(ctx context.Context, spec *ChangesetSpec) (ChangesetSpecID, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", errors.Wrap(err, "marshalling changeset spec JSON")
	}

	var result struct {
		CreateChangesetSpec struct {
			ID string
		}
	}
	if ok, err := svc.client.NewRequest(createChangesetSpecMutation, map[string]interface{}{
		"spec": string(raw),
	}).Do(ctx, &result); err != nil || !ok {
		return "", err
	}

	return ChangesetSpecID(result.CreateChangesetSpec.ID), nil
}

func (svc *Service) NewExecutionCache(dir string) ExecutionCache {
	if dir == "" {
		return &ExecutionNoOpCache{}
	}

	return &ExecutionDiskCache{dir}
}

type ExecutorOpts struct {
	Cache       ExecutionCache
	Parallelism int
	Timeout     time.Duration

	ClearCache    bool
	KeepLogs      bool
	VerboseLogger bool
}

func (svc *Service) NewExecutor(opts ExecutorOpts, update ExecutorUpdateCallback) Executor {
	return newExecutor(opts, svc.client, update)
}

func (svc *Service) ExecuteCampaignSpec(ctx context.Context, x Executor, spec *CampaignSpec) ([]*ChangesetSpec, error) {
	repos, err := svc.ResolveRepositories(ctx, spec)
	if err != nil {
		return nil, errors.Wrap(err, "resolving repositories")
	}

	// TODO: split into a separate function
	// TODO: status logging
	for i, step := range spec.Steps {
		image, err := getDockerImageContentDigest(ctx, step.Container)
		if err != nil {
			return nil, errors.Wrapf(err, "step %d", i+1)
		}
		spec.Steps[i].image = image
	}

	for _, repo := range repos {
		x.AddTask(repo, spec.Steps, spec.ChangesetTemplate)
	}

	x.Start(ctx)
	specs, err := x.Wait()
	if err != nil {
		return nil, errors.Wrap(err, "executing campaign spec")
	}

	// Add external changeset specs.
	for _, ic := range spec.ImportChangesets {
		repo, err := svc.resolveRepositoryName(ctx, ic.Repository)
		if err != nil {
			return nil, errors.Wrapf(err, "resolving repository name %q", ic.Repository)
		}

		fmt.Println(*repo)

		for _, id := range ic.ExternalIDs {
			var sid string

			switch tid := id.(type) {
			case string:
				sid = tid
			case int, int8, int16, int32, int64:
				sid = strconv.FormatInt(reflect.ValueOf(id).Int(), 10)
			case uint, uint8, uint16, uint32, uint64:
				sid = strconv.FormatUint(reflect.ValueOf(id).Uint(), 10)
			case float32:
				sid = strconv.FormatFloat(float64(tid), 'f', -1, 32)
			case float64:
				sid = strconv.FormatFloat(tid, 'f', -1, 64)
			default:
				return nil, errors.Errorf("cannot convert value of type %T into a valid external ID: expected string or int", id)
			}

			specs = append(specs, &ChangesetSpec{
				BaseRepository:    repo.ID,
				ExternalChangeset: &ExternalChangeset{sid},
			})
		}
	}

	return specs, nil
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

const namespaceQuery = `
query NamespaceQuery($name: String!) {
    user(username: $name) {
        id
    }

    organization(name: $name) {
        id
    }
}
`

func (svc *Service) ResolveNamespace(ctx context.Context, namespace string) (string, error) {
	var result struct {
		Data struct {
			User         *struct{ ID string }
			Organization *struct{ ID string }
		}
		Errors []interface{}
	}
	if ok, err := svc.client.NewRequest(namespaceQuery, map[string]interface{}{
		"name": namespace,
	}).DoRaw(ctx, &result); err != nil || !ok {
		return "", err
	}

	if result.Data.User != nil {
		return result.Data.User.ID, nil
	}
	if result.Data.Organization != nil {
		return result.Data.Organization.ID, nil
	}
	return "", errors.New("no user or organization found")
}

func (svc *Service) ResolveRepositories(ctx context.Context, spec *CampaignSpec) ([]*Repository, error) {
	final := []*Repository{}
	seen := map[string]struct{}{}

	// TODO: this could be trivially parallelised in the future.
	for _, on := range spec.On {
		repos, err := svc.ResolveRepositoriesOn(ctx, &on)
		if err != nil {
			return nil, errors.Wrapf(err, "resolving %q", on.Label())
		}

		for _, repo := range repos {
			if _, ok := seen[repo.ID]; !ok {
				seen[repo.ID] = struct{}{}
				final = append(final, repo)
			}
		}
	}

	return final, nil
}

func (svc *Service) ResolveRepositoriesOn(ctx context.Context, on *OnQueryOrRepository) ([]*Repository, error) {
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
	var result struct{ Repository *Repository }
	if ok, err := svc.client.NewRequest(repositoryNameQuery, map[string]interface{}{
		"name": name,
	}).Do(ctx, &result); err != nil || !ok {
		return nil, err
	}
	if result.Repository == nil {
		return nil, errors.New("no repository found")
	}
	return result.Repository, nil
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
		"query": setDefaultQueryCount(query),
	}).Do(ctx, &result); err != nil || !ok {
		return nil, err
	}

	ids := map[string]struct{}{}
	var repos []*Repository
	for _, r := range result.Search.Results.Results {
		if _, ok := ids[r.ID]; !ok {
			repo := r.Repository
			repos = append(repos, &repo)
			ids[r.ID] = struct{}{}
		}
	}
	return repos, nil
}

var defaultQueryCountRegex = regexp.MustCompile(`\bcount:\d+\b`)

const hardCodedCount = " count:999999"

func setDefaultQueryCount(query string) string {
	if defaultQueryCountRegex.MatchString(query) {
		return query
	}

	return query + hardCodedCount
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
