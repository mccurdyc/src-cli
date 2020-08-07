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

		for _, on := range spec.On {
			repos, err := svc.ResolveRepositories(ctx, &on)
			if err != nil {
				return err
			}
			for _, repo := range repos {
				fmt.Fprintf(out, "%s\t%s\n", repo.ID, repo.Name)
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
