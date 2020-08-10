package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/pkg/errors"
	"github.com/sourcegraph/src-cli/internal/api"
	"github.com/sourcegraph/src-cli/internal/campaigns"
)

func init() {
	usage := `
Examples go here
`

	flagSet := flag.NewFlagSet("repositories", flag.ExitOnError)

	var (
		fileFlag = flagSet.String("f", "", "The campaign spec file to read.")
		apiFlags = api.NewFlags(flagSet)
	)

	handler := func(args []string) error {
		if err := flagSet.Parse(args); err != nil {
			return err
		}

		specFile, err := campaignsOpenFileFlag(fileFlag)
		if err != nil {
			return err
		}
		defer specFile.Close()

		ctx := context.Background()
		client := cfg.apiClient(apiFlags, flagSet.Output())

		svc := campaigns.NewService(&campaigns.ServiceOpts{Client: client})
		spec, err := svc.ParseCampaignSpec(specFile)
		if err != nil {
			return errors.Wrap(err, "parsing campaign spec")
		}

		out := flagSet.Output()
		if err := campaignsValidateSpec(out, spec); err != nil {
			return err
		}

		tmpl, err := parseTemplate(campaignsRepositoriesTemplate)
		if err != nil {
			return err
		}

		for _, on := range spec.On {
			repos, err := svc.ResolveRepositories(ctx, &on)
			if err != nil {
				return err
			}

			max := 0
			for _, repo := range repos {
				if len(repo.Name) > max {
					max = len(repo.Name)
				}
			}

			if err := execTemplate(tmpl, struct {
				Max                 int
				Query               string
				RepoCount           int
				Repos               []*campaigns.Repository
				SourcegraphEndpoint string
			}{
				Max:                 max,
				Query:               on.Label(),
				RepoCount:           len(repos),
				Repos:               repos,
				SourcegraphEndpoint: cfg.Endpoint,
			}); err != nil {
				return err
			}
		}

		return nil
	}

	campaignsCommands = append(campaignsCommands, &command{
		flagSet: flagSet,
		aliases: []string{"repos"},
		handler: handler,
		usageFunc: func() {
			fmt.Fprintf(flag.CommandLine.Output(), "Usage of 'src campaigns %s':\n", flagSet.Name())
			flagSet.PrintDefaults()
			fmt.Println(usage)
		},
	})
}

const campaignsRepositoriesTemplate = `
{{- color "logo" -}}✱{{- color "nc" -}}
{{- " " -}}
{{- if eq .RepoCount 0 -}}
    {{- color "warning" -}}
{{- else -}}
    {{- color "success" -}}
{{- end -}}
{{- .RepoCount }} result{{ if ne .RepoCount 1 }}s{{ end }}{{- color "nc" -}}
{{- " for " -}}{{- color "search-query"}}"{{.Query}}"{{color "nc"}}{{"\n" -}}

{{- range .Repos -}}
    {{- "  "}}{{ color "success" }}{{ padRight .Name $.Max " " }}{{ color "nc" -}}
    {{- color "search-border"}}{{" ("}}{{color "nc" -}}
    {{- color "search-repository"}}{{$.SourcegraphEndpoint}}{{.URL}}{{color "nc" -}}
    {{- color "search-border"}}{{")\n"}}{{color "nc" -}}
{{- end -}}
`
