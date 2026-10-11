package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
)

// runSetupDailyLimit sets the account's daily spend cap. When today's cost
// reaches it the daemon refuses new runs and stops running ones.
func runSetupDailyLimit(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("setup daily-limit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private daemon Unix socket")
	usd := fs.Float64("usd", 0, "daily spend cap in US dollars (0 turns the cap off)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(output)
			fs.PrintDefaults()
			return nil
		}
		return errors.New("invalid daily-limit flags; use --help")
	}
	if fs.NArg() != 0 || *usd < 0 || *usd > 100000 {
		return errors.New("usage: swarmctl setup daily-limit --usd 25")
	}
	client, err := newSetupClient(*socket)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	var result struct {
		Limits struct {
			DailyCostLimitUSD float64 `json:"daily_cost_limit_usd"`
			Enabled           bool    `json:"enabled"`
			TodayCostUSD      float64 `json:"today_cost_usd"`
		} `json:"limits"`
	}
	if err := setupRequest(client, http.MethodPost, "/v3/sessions:usage-limits", map[string]any{"daily_cost_limit_usd": *usd, "enabled": *usd > 0}, &result); err != nil {
		return err
	}
	if *usd == 0 {
		_, err = fmt.Fprintln(output, "Daily spend cap off.")
		return err
	}
	_, err = fmt.Fprintf(output, "Daily spend cap $%.2f (spent today: $%.4f).\n", result.Limits.DailyCostLimitUSD, result.Limits.TodayCostUSD)
	return err
}
