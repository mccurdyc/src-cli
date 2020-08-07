package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"github.com/sourcegraph/src-cli/internal/campaigns"
)

func init() {
	usage := `
Examples go here
`

	flagSet := flag.NewFlagSet("validate", flag.ExitOnError)
	fileFlag := flagSet.String("f", "", "The campaign spec file to read.")

	handler := func(args []string) error {
		if err := flagSet.Parse(args); err != nil {
			return err
		}

		specFile, err := campaignsOpenFileFlag(fileFlag)
		if err != nil {
			return err
		}
		defer specFile.Close()

		svc := campaigns.NewService(&campaigns.ServiceOpts{})
		spec, err := svc.ParseCampaignSpec(specFile)
		if err != nil {
			return errors.Wrap(err, "parsing campaign spec")
		}

		out := flagSet.Output()
		if err := campaignsValidateSpec(out, spec); err != nil {
			return err
		}

		if colorDisabled {
			fmt.Fprintln(out, "Campaign spec successfully validated.")
		} else {
			fmt.Fprintf(out, "%s\u2705 Campaign spec successfully validated.%s\n", ansiColors["success"], ansiColors["nc"])
		}

		return nil
	}

	campaignsCommands = append(campaignsCommands, &command{
		flagSet: flagSet,
		handler: handler,
		usageFunc: func() {
			fmt.Fprintf(flag.CommandLine.Output(), "Usage of 'src campaigns %s':\n", flagSet.Name())
			flagSet.PrintDefaults()
			fmt.Println(usage)
		},
	})
}

// campaignsValidateSpec validates the given campaign spec. If the spec has
// validation errors, they are output in a human readable form and an
// exitCodeError is returned.
func campaignsValidateSpec(out io.Writer, spec *campaigns.CampaignSpec) error {
	if err := spec.Validate(); err != nil {
		if merr, ok := err.(*multierror.Error); ok {
			if colorDisabled {
				fmt.Fprintln(out, "Campaign spec failed validation.")
			} else {
				fmt.Fprintf(out, "%s\u274c Campaign spec failed validation.%s\n", ansiColors["warning"], ansiColors["nc"])
			}
			for i, err := range merr.Errors {
				fmt.Fprintf(out, "   %d. %s\n", i+1, err)
			}

			return &exitCodeError{
				error:    nil,
				exitCode: 2,
			}
		} else {
			// This shouldn't happen; let's just punt and let the normal
			// rendering occur.
			return err
		}
	}

	return nil
}
