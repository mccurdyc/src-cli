package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"github.com/sourcegraph/src-cli/internal/api"
	"github.com/sourcegraph/src-cli/internal/campaigns"
)

func init() {
	usage := `
Examples go here
`

	cacheDir := defaultCacheDir()

	flagSet := flag.NewFlagSet("apply", flag.ExitOnError)
	var (
		cacheDirFlag    = flagSet.String("cache", cacheDir, "Directory for caching results.")
		fileFlag        = flagSet.String("f", "", "The campaign spec file to read.")
		keepFlag        = flagSet.Bool("keep-logs", false, "Retain logs after executing steps.")
		namespaceFlag   = flagSet.String("namespace", "", "The user or organization namespace to place the campaign within.")
		parallelismFlag = flagSet.Int("j", 0, "The maximum number of parallel jobs. (Default: GOMAXPROCS.)")
		previewFlag     = flagSet.Bool("preview", false, "Display a preview URL for the campaign after applying the campaign spec.")
		timeoutFlag     = flagSet.Duration("timeout", 60*time.Minute, "The maximum duration a single set of campaign steps can take.")
		apiFlags        = api.NewFlags(flagSet)
	)

	handler := func(args []string) error {
		if err := flagSet.Parse(args); err != nil {
			return err
		}

		ctx := context.Background()
		client := cfg.apiClient(apiFlags, flagSet.Output())
		out := flagSet.Output()

		// Parse flags and build up our service options.
		var errs *multierror.Error
		svc := campaigns.NewService(&campaigns.ServiceOpts{
			Client: client,
		})

		specFile, err := campaignsOpenFileFlag(fileFlag)
		if err != nil {
			errs = multierror.Append(errs, err)
		} else {
			defer specFile.Close()
		}

		if namespaceFlag == nil || *namespaceFlag == "" {
			errs = multierror.Append(errs, &usageError{errors.New("a namespace must be provided with -namespace")})
		}

		opts := campaigns.ExecutorOpts{
			Cache:    svc.NewExecutionCache(*cacheDirFlag),
			KeepLogs: *keepFlag,
			Timeout:  *timeoutFlag,
		}
		if parallelismFlag != nil || *parallelismFlag <= 0 {
			opts.Parallelism = runtime.GOMAXPROCS(0)
		} else {
			opts.Parallelism = *parallelismFlag
		}
		executor := svc.NewExecutor(opts, nil)

		if previewFlag == nil || !*previewFlag {
		}

		if errs != nil {
			return errs
		}

		applyStatus(out, "  ", ansiColors["warning"], "parsing campaign spec")
		campaignSpec, err := svc.ParseCampaignSpec(specFile)
		if err != nil {
			return errors.Wrap(err, "parsing campaign spec")
		}

		if err := campaignsValidateSpec(out, campaignSpec); err != nil {
			return err
		}
		applyStatus(out, "  ", ansiColors["success"], "campaign spec parsed and validated")

		applyStatus(out, "  ", ansiColors["warning"], "resolving repositories")
		repos, err := svc.ResolveRepositories(ctx, campaignSpec)
		if err != nil {
			return err
		}
		plural := "ies"
		if len(repos) == 1 {
			plural = "y"
		}
		applyStatus(out, "  ", ansiColors["success"], "%d repositor%s resolved", len(repos), plural)

		specs, err := svc.ExecuteCampaignSpec(ctx, executor, campaignSpec)
		if err != nil {
			return err
		}
		for _, spec := range specs {
			fmt.Fprintln(out, *spec)
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

func applyStatus(w io.Writer, emoji, color, format string, a ...interface{}) {
	if *verbose {
		fmt.Fprintf(w, "%s%s", emoji, color)
		fmt.Fprintf(w, format, a...)
		fmt.Fprintln(w, ansiColors["nc"])
	}
}

func defaultCacheDir() string {
	uc, err := os.UserCacheDir()
	if err != nil {
		return ""
	}

	return path.Join(uc, "sourcegraph", "campaigns")
}
