package main

import (
	"fmt"
	"go/token"
	"os"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"golang.org/x/text/language"

	"github.com/PaulSonOfLars/goloc/pkg/loc"
)

func ingestFlagSlices(funcsSlice, fmtfuncsSlice *[]string, l *loc.Locer) {
	l.Funcs = make(map[string]struct{})
	l.Fmtfuncs = make(map[string]struct{})
	for src, dest := range map[*[]string]map[string]struct{}{
		funcsSlice: l.Funcs, fmtfuncsSlice: l.Fmtfuncs} {
		if *src == nil {
			continue
		}
		if len(*src) == 0 {
			continue
		}
		for _, f := range *src {
			dest[f] = struct{}{}
		}
	}
}

func ingestFlagLog(debug, trace bool) {
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}
	if trace {
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	}
}

func ingestFlagLang(lang string, l *loc.Locer) {
	if lang == "" {
		return
	}
	l.DefaultLang = lang
}

func main() {
	l := &loc.Locer{
		DefaultLang: "en-GB",
		Checked:     make(map[string]struct{}),
		Fset:        token.NewFileSet(),
	}

	var (
		lang          string
		debug         = false
		trace         = false
		funcsSlice    = make([]string, 0)
		fmtfuncsSlice = make([]string, 0)
		log           = loc.Logger
	)

	rootCmd := cobra.Command{
		Use:   "goloc",
		Short: "Extract strings for i18n of your go tools",
		Long:  "Simple i18n tool to allow for extracting all your i18n strings into manageable files, and load them back after.",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			ingestFlagLog(debug, trace)
			ingestFlagLang(lang, l)
			ingestFlagSlices(&funcsSlice, &fmtfuncsSlice, l)
		},
	}

	rootCmd.PersistentFlags().StringSliceVar(&funcsSlice, "funcs", nil, "all funcs to extraxt")
	rootCmd.PersistentFlags().StringSliceVar(&fmtfuncsSlice, "fmtfuncs", nil, "all format funcs to extract")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "v", false, "add extra verbosity")
	rootCmd.PersistentFlags().BoolVarP(&trace, "trace", "V", false, "add trace verbosity")
	rootCmd.PersistentFlags().BoolVarP(&l.Apply, "apply", "a", false, "save to file")
	rootCmd.PersistentFlags().StringVarP(&lang, "lang", "l", language.BritishEnglish.String(), "")

	rootCmd.AddCommand(&cobra.Command{
		Use:   "inspect",
		Short: "Run an analyse all appropriate strings in specified files",
		Run: func(cmd *cobra.Command, args []string) {
			if err := l.Handle(args, l.Inspect); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "extract",
		Short: "extract all strings",
		Run: func(cmd *cobra.Command, args []string) {
			if err := l.Handle(args, l.Fix); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	})

	createLang := ""
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "create new language from default",
		Run: func(cmd *cobra.Command, args []string) {
			if createLang == "" {
				log.Error().Msg("No language to create specified")
				return
			}
			var langTag language.Tag
			if langTag = language.Make(createLang); langTag == language.Und {
				log.Fatal().Msgf("invalid language selected: '%v' does not match any known language codes", lang)
			}

			l.Create(args, langTag)
		},
	}
	createCmd.Flags().StringVarP(&createLang, "create", "c", "", "select which language to create")
	rootCmd.AddCommand(createCmd)

	checkLang := "all"
	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Check integrity of language files",
		Run: func(cmd *cobra.Command, args []string) {
			var err error
			if checkLang == "all" {
				err = l.CheckAll()
				// load all, iterate over language code
				// check all
			} else {
				// load default, and load lang, check
				err = l.Check(checkLang)
			}
			if err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}
	checkCmd.Flags().StringVarP(&checkLang, "check", "c", "all", "select which language to check")
	rootCmd.AddCommand(checkCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
