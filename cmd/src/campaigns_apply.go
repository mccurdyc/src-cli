package main

import (
	"flag"
	"fmt"
	"runtime"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"github.com/sourcegraph/src-cli/internal/campaigns"
)

func init() {
	usage := `
Examples go here
`

	flagSet := flag.NewFlagSet("apply", flag.ExitOnError)
	var (
		fileFlag        = flagSet.String("f", "", "The campaign spec file to read.")
		namespaceFlag   = flagSet.String("namespace", "", "The user or organization namespace to place the campaign within.")
		parallelismFlag = flagSet.Int("j", 0, "The maximum number of parallel jobs. (Default: GOMAXPROCS.)")
		previewFlag     = flagSet.Bool("preview", false, "Display a preview URL for the campaign after applying the campaign spec.")
	)

	handler := func(args []string) error {
		if err := flagSet.Parse(args); err != nil {
			return err
		}

		// Parse flags and build up our service options.
		var errs *multierror.Error
		opts := &campaigns.ServiceOpts{
			ServiceExecutorOpts: campaigns.ServiceExecutorOpts{
				ExecutorOpts: campaigns.ExecutorOpts{},
			},
		}

		specFile, err := campaignsOpenFileFlag(fileFlag)
		if err != nil {
			errs = multierror.Append(errs, err)
		} else {
			defer specFile.Close()
		}

		if namespaceFlag == nil || *namespaceFlag == "" {
			errs = multierror.Append(errs, &usageError{errors.New("a namespace must be provided with -namespace")})
		}

		if parallelismFlag != nil || *parallelismFlag <= 0 {
			opts.ServiceExecutorOpts.Parallelism = runtime.GOMAXPROCS(0)
		} else {
			opts.ServiceExecutorOpts.Parallelism = *parallelismFlag
		}

		if previewFlag == nil || !*previewFlag {
		}

		if errs != nil {
			return errs
		}

		svc := campaigns.NewService(opts)
		campaignSpec, err := svc.ParseCampaignSpec(specFile)
		if err != nil {
			return errors.Wrap(err, "parsing campaign spec")
		}

		out := flagSet.Output()
		if err := campaignsValidateSpec(out, campaignSpec); err != nil {
			return err
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
