package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/ghodss/yaml"
	jsoniter "github.com/json-iterator/go"
	"github.com/mattn/go-isatty"
)

type validationSpec struct {
	FirstAdmin struct {
		Email    string
		Username string
		Password string
	}
	WaitRepoCloned struct {
		Repo                     string
		MaxTries                 int
		SleepBetweenTriesSeconds int
	}
	SearchQuery     string
	ExternalService struct {
		Kind           string
		DisplayName    string
		Config         *json.RawMessage
		DeleteWhenDone bool
	}
}

type validator struct {
	client *vdClient
}

func init() {
	usage := `'src validate' is a tool that validates a Sourcegraph instance.

EXPERIMENTAL: 'validate' is an experimental command in the 'src' tool.

Usage:

	src validate [options] <script-file>
or
    cat <script-file> | src validate [options]
`
	flagSet := flag.NewFlagSet("validate", flag.ExitOnError)
	usageFunc := func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(), "Usage of 'src validate %s':\n", flagSet.Name())
		flagSet.PrintDefaults()
		fmt.Println(usage)
	}

	var (
		contextFlag = flagSet.String("context", "", `Comma-separated list of key=value pairs to add to the script execution context`)
		docFlag     = flagSet.Bool("doc", false, `Show documentation`)
		secretsFlag = flagSet.String("secrets", "", "Path to a file containing key=value lines. The key value pairs will be added to the script context")
	)

	vd := &validator{}

	commands = append(commands, &command{
		flagSet: flagSet,
		handler: func(args []string) error {
			if *docFlag {
				vd.printDocumentation()
				return nil
			}

			var script []byte
			var err error
			if len(flagSet.Args()) == 1 {
				filename := flagSet.Arg(0)
				script, err = ioutil.ReadFile(filename)
				if err != nil {
					return err
				}
				if strings.HasSuffix(filename, ".yaml") || strings.HasSuffix(filename, ".yml") {
					script, err = yaml.YAMLToJSONStrict(script)
					if err != nil {
						return err
					}
				}
			}
			if !isatty.IsTerminal(os.Stdin.Fd()) {
				// stdin is a pipe not a terminal
				script, err = ioutil.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				script, err = yaml.YAMLToJSONStrict(script)
				if err != nil {
					return err
				}
			}

			ctxm := vd.parseKVPairs(*contextFlag, ",")

			if *secretsFlag != "" {
				sm, err := vd.readSecrets(*secretsFlag)
				if err != nil {
					return err
				}

				for k, v := range sm {
					ctxm[k] = v
				}
			}

			return vd.validate(script, ctxm)
		},
		usageFunc: usageFunc,
	})
}

func (vd *validator) printDocumentation() {
	fmt.Println("Please visit https://docs.sourcegraph.com/admin/validation for documentation of the validate command.")
}

func (vd *validator) parseKVPairs(val string, pairSep string) map[string]string {
	scriptContext := make(map[string]string)

	pairs := strings.Split(val, pairSep)
	for _, pair := range pairs {
		kv := strings.Split(pair, "=")

		if len(kv) == 2 {
			scriptContext[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return scriptContext
}

func (vd *validator) readSecrets(path string) (map[string]string, error) {
	bs, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return vd.parseKVPairs(string(bs), "\n"), nil
}

func (vd *validator) validate(script []byte, scriptContext map[string]string) error {
	tpl, err := template.New("validate").Parse(string(script))
	if err != nil {
		return err
	}
	var ts bytes.Buffer
	err = tpl.Execute(&ts, scriptContext)
	if err != nil {
		return err
	}

	var vspec validationSpec
	if err := json.Unmarshal(ts.Bytes(), &vspec); err != nil {
		return err
	}

	if vspec.FirstAdmin.Username != "" {
		err = vd.createFirstAdmin(&vspec)
		if err != nil {
			return err
		}
	}

	if vspec.ExternalService.DisplayName != "" {
		extSvcID, err := vd.addExternalService(&vspec)
		if err != nil {
			return err
		}

		defer func() {
			if extSvcID != "" && vspec.ExternalService.DeleteWhenDone {
				_ = vd.deleteExternalService(extSvcID)
			}
		}()
	}

	if vspec.WaitRepoCloned.Repo != "" {
		cloned, err := vd.waitRepoCloned(vspec.WaitRepoCloned.Repo, vspec.WaitRepoCloned.SleepBetweenTriesSeconds,
			vspec.WaitRepoCloned.MaxTries)
		if err != nil {
			return err
		}
		if !cloned {
			return fmt.Errorf("repo %s didn't clone", vspec.WaitRepoCloned.Repo)
		}
	}

	if vspec.SearchQuery != "" {
		matchCount, err := vd.searchMatchCount(vspec.SearchQuery)
		if err != nil {
			return err
		}
		if matchCount == 0 {
			return fmt.Errorf("search query %s returned no results", vspec.SearchQuery)
		}
	}

	return nil
}

const vdAddExternalServiceQuery = `
mutation AddExternalService($kind: ExternalServiceKind!, $displayName: String!, $config: String!) {
  addExternalService(input:{
    kind:$kind,
    displayName:$displayName,
    config: $config
  })
  {
    id
  }
}`

func (vd *validator) addExternalService(vspec *validationSpec) (string, error) {
	configJson, err := vspec.ExternalService.Config.MarshalJSON()
	if err != nil {
		return "", err
	}
	var resp struct {
		AddExternalService struct {
			ID string `json:"id"`
		} `json:"addExternalService"`
	}

	err = vd.graphQL(vdAddExternalServiceQuery, map[string]interface{}{
		"kind":        vspec.ExternalService.Kind,
		"displayName": vspec.ExternalService.DisplayName,
		"config":      string(configJson),
	}, &resp)

	return resp.AddExternalService.ID, err
}

const vdDeleteExternalServiceQuery = `
mutation DeleteExternalService($id: ID!) {
  deleteExternalService(externalService: $id){
    alwaysNil
  } 
}`

func (vd *validator) deleteExternalService(id string) error {
	var resp struct{}

	return vd.graphQL(vdDeleteExternalServiceQuery, map[string]interface{}{
		"id": id,
	}, &resp)
}

const vdSearchMatchCountQuery = `
query ($query: String!) {
  search(query: $query, version: V2, patternType:literal) {
    results {
      matchCount
    }
  }
}`

func (vd *validator) searchMatchCount(searchStr string) (int, error) {
	var resp struct {
		Search struct {
			Results struct {
				MatchCount int `json:"matchCount"`
			} `json:"results"`
		} `json:"search"`
	}

	err := vd.graphQL(vdSearchMatchCountQuery, map[string]interface{}{
		"query": searchStr,
	}, &resp)

	return resp.Search.Results.MatchCount, err
}

const vdListRepos = `
query ListRepos($cloneInProgress: Boolean!, $cloned: Boolean!, $notCloned: Boolean!, $names: [String!]) {
  repositories(
    cloned: $cloneInProgress
    cloneInProgress: $cloned
    notCloned: $notCloned
    names: $names
  ) {
    nodes {
      name
    }
  }
}`

func (vd *validator) listClonedRepos(fs []string) ([]string, error) {
	var resp struct {
		Repositories struct {
			Nodes []struct {
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"repositories"`
	}

	err := vd.graphQL(vdListRepos, map[string]interface{}{
		"cloneInProgress": true,
		"cloned":          true,
		"notCloned":       true,
		"names":           fs,
	}, &resp)

	names := make([]string, 0, len(resp.Repositories.Nodes))
	for _, node := range resp.Repositories.Nodes {
		names = append(names, node.Name)
	}

	return names, err
}

func (vd *validator) waitRepoCloned(repoName string, sleepSeconds int, maxTries int) (bool, error) {
	nameFilter := []string{repoName}

	for i := 0; i < maxTries; i++ {
		names, err := vd.listClonedRepos(nameFilter)
		if err != nil {
			return false, err
		}
		if len(names) == 1 {
			return true, nil
		}
		time.Sleep(time.Second * time.Duration(sleepSeconds))
	}
	return false, nil
}

// SiteAdminInit initializes the instance with given admin account.
// It returns an authenticated client as the admin for doing e2e testing.
func (vd *validator) siteAdminInit(baseURL, email, username, password string) (*vdClient, error) {
	client, err := vd.newClient(baseURL)
	if err != nil {
		return nil, err
	}

	var request = struct {
		Email    string `json:"email"`
		Username string `json:"username"`
		Password string `json:"password"`
	}{
		Email:    email,
		Username: username,
		Password: password,
	}
	err = client.authenticate("/-/site-init", request)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// SignIn performs the sign in with given user credentials.
// It returns an authenticated client as the user for doing e2e testing.
func (vd *validator) signIn(baseURL string, email, password string) (*vdClient, error) {
	client, err := vd.newClient(baseURL)
	if err != nil {
		return nil, err
	}

	var request = struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}{
		Email:    email,
		Password: password,
	}
	err = client.authenticate("/-/sign-in", request)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// extractCSRFToken extracts CSRF token from HTML response body.
func (vd *validator) extractCSRFToken(body string) string {
	anchor := `X-Csrf-Token":"`
	i := strings.Index(body, anchor)
	if i == -1 {
		return ""
	}

	j := strings.Index(body[i+len(anchor):], `","`)
	if j == -1 {
		return ""
	}

	return body[i+len(anchor) : i+len(anchor)+j]
}

// Client is an authenticated client for a Sourcegraph user for doing e2e testing.
// The user may or may not be a site admin depends on how the client is instantiated.
// It works by simulating how the browser would send HTTP requests to the server.
type vdClient struct {
	baseURL       string
	csrfToken     string
	csrfCookie    *http.Cookie
	sessionCookie *http.Cookie

	userID string
}

// newClient instantiates a new client by performing a GET request then obtains the
// CSRF token and cookie from its response.
func (vd *validator) newClient(baseURL string) (*vdClient, error) {
	resp, err := http.Get(baseURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	p, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	csrfToken := vd.extractCSRFToken(string(p))
	if csrfToken == "" {
		return nil, err
	}
	var csrfCookie *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "sg_csrf_token" {
			csrfCookie = cookie
			break
		}
	}
	if csrfCookie == nil {
		return nil, errors.New(`"sg_csrf_token" cookie not found`)
	}

	return &vdClient{
		baseURL:    baseURL,
		csrfToken:  csrfToken,
		csrfCookie: csrfCookie,
	}, nil
}

// authenticate is used to send a HTTP POST request to an URL that is able to authenticate
// a user with given body (marshalled to JSON), e.g. site admin init, sign in. Once the
// client is authenticated, the session cookie will be stored as a proof of authentication.
func (c *vdClient) authenticate(path string, body interface{}) error {
	p, err := jsoniter.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(p))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Csrf-Token", c.csrfToken)
	req.AddCookie(c.csrfCookie)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		p, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return errors.New(string(p))
	}

	var sessionCookie *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "sgs" {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil {
		return err
	}
	c.sessionCookie = sessionCookie

	userID, err := c.currentUserID()
	if err != nil {
		return err
	}
	c.userID = userID
	return nil
}

// currentUserID returns the current user's GraphQL node ID.
func (c *vdClient) currentUserID() (string, error) {
	query := `
	query {
		currentUser {
			id
		}
	}
`
	var resp struct {
		Data struct {
			CurrentUser struct {
				ID string `json:"id"`
			} `json:"currentUser"`
		} `json:"data"`
	}
	err := c.graphQL("", query, nil, &resp)
	if err != nil {
		return "", err
	}

	return resp.Data.CurrentUser.ID, nil
}

// GraphQL makes a GraphQL request to the server on behalf of the user authenticated by the client.
// An optional token can be passed to impersonate other users.
func (c *vdClient) graphQL(token, query string, variables map[string]interface{}, target interface{}) error {
	body, err := jsoniter.Marshal(map[string]interface{}{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/.api/graphql", c.baseURL), bytes.NewReader(body))
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("token %s", token))
	} else {
		// NOTE: We use this header to protect from CSRF attacks of HTTP API,
		// see https://sourcegraph.com/github.com/sourcegraph/sourcegraph/-/blob/cmd/frontend/internal/cli/http.go#L41-42
		req.Header.Set("X-Requested-With", "Sourcegraph")
		req.AddCookie(c.csrfCookie)
		req.AddCookie(c.sessionCookie)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		p, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return errors.New(string(p))
	}

	// Decode the response.
	var result struct {
		Data   interface{} `json:"data,omitempty"`
		Errors interface{} `json:"errors,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if result.Errors != nil {
		return &exitCodeError{
			error:    fmt.Errorf("GraphQL errors:\n%s", &graphqlError{result.Errors}),
			exitCode: graphqlErrorsExitCode,
		}
	}
	if err := jsonCopy(target, result.Data); err != nil {
		return err
	}
	return nil
}

func (vd *validator) createFirstAdmin(vspec *validationSpec) error {
	client, err := vd.signIn(cfg.Endpoint, vspec.FirstAdmin.Email, vspec.FirstAdmin.Password)
	if err != nil {
		client, err = vd.siteAdminInit(cfg.Endpoint, vspec.FirstAdmin.Email, vspec.FirstAdmin.Username,
			vspec.FirstAdmin.Password)
		if err != nil {
			return err
		}
	}

	vd.client = client
	return nil
}

func (vd *validator) graphQL(query string, variables map[string]interface{}, target interface{}) error {
	if vd.client != nil {
		return vd.client.graphQL("", query, variables, target)
	}

	return (&apiRequest{
		query:  query,
		vars:   variables,
		result: target,
	}).do()
}