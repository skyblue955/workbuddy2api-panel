// Command openapi exposes only the official, explicitly opted-in API actions.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/activities"
	"github.com/linguo2625469/workbuddy2api-panel/internal/workbuddyopen"
)

const maxInput = 64 << 10

type redeemer interface {
	Redeem(context.Context, string, workbuddyopen.RedemptionRequest) (*workbuddyopen.RedemptionResponse, error)
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, workbuddyopen.NewClient()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, out io.Writer, client redeemer) error {
	if len(args) == 0 {
		return errors.New("usage: openapi activities | authorize | exchange | redeem [-execute -token-file path]")
	}
	switch args[0] {
	case "authorize", "exchange":
		oauthClient, ok := client.(*workbuddyopen.Client)
		if !ok {
			return errors.New("oauth: client does not support authorization")
		}
		if args[0] == "authorize" {
			return runAuthorize(args[1:], out, oauthClient)
		}
		return runExchange(args[1:], stdin, out, oauthClient)
	case "activities":
		fs := flag.NewFlagSet("activities", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return errors.New("activities: unknown or trailing arguments")
		}
		return json.NewEncoder(out).Encode(activities.Catalog(time.Now()))
	case "redeem":
		return runRedeem(args[1:], stdin, out, client)
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runRedeem(args []string, stdin io.Reader, out io.Writer, client redeemer) error {
	fs := flag.NewFlagSet("redeem", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	execute := fs.Bool("execute", false, "submit the request")
	tokenFile := fs.String("token-file", "", "official OAuth access-token JSON file")
	journalDir := fs.String("journal-dir", "", "durable redemption journal directory")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("redeem: unknown or trailing arguments")
	}
	input, err := decodeRequest(stdin)
	if err != nil {
		return err
	}
	if err := input.Validate(); err != nil {
		return err
	}
	if !*execute {
		if _, err := io.WriteString(out, "DRY_RUN: no network request\n"); err != nil {
			return err
		}
		return nil
	}
	if strings.TrimSpace(*tokenFile) == "" {
		return errors.New("redeem: -token-file is required with -execute")
	}
	if strings.TrimSpace(*journalDir) == "" {
		return errors.New("redeem: -journal-dir is required with -execute")
	}
	normalizedJournal, pathErr := filepath.Abs(*journalDir)
	if pathErr != nil {
		return errors.New("redeem: journal directory is invalid")
	}
	*journalDir = normalizedJournal

	doc, err := readTokenDocument(*tokenFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(doc.OpenID) == "" {
		return errors.New("redeem: token file has no open_id")
	}
	rec, path, beginErr := beginRedemption(*journalDir, input, doc.OpenID)
	if rec.State == "succeeded" && beginErr == nil {
		result := replayResult(rec)
		_, err = fmt.Fprintf(out, "{\"flow_no\":%q,\"credits\":%d}\n", result.FlowNo, result.Credits)
		return err
	}
	if beginErr != nil {
		return beginErr
	}
	result, err := client.Redeem(context.Background(), doc.AccessToken, input)
	if err == nil {
		err = validateRedemptionResult(result)
	}
	if err == nil && result.OpenID != doc.OpenID {
		err = errors.New("redeem: response subject mismatch")
	}
	if err != nil {
		return err
	}
	if err := completeRedemption(path, *journalDir, rec, result); err != nil {
		return errors.New("redeem: result durability failed")
	}
	_, err = fmt.Fprintf(out, "{\"flow_no\":%q,\"credits\":%d}\n", result.FlowNo, result.Credits)
	return err
}

func decodeRequest(r io.Reader) (workbuddyopen.RedemptionRequest, error) {
	limited := io.LimitReader(r, maxInput+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return workbuddyopen.RedemptionRequest{}, errors.New("redeem: input unreadable")
	}
	if len(data) > maxInput {
		return workbuddyopen.RedemptionRequest{}, errors.New("redeem: input exceeds 64 KiB")
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var input workbuddyopen.RedemptionRequest
	if err := dec.Decode(&input); err != nil {
		return input, errors.New("redeem: input must be one JSON object")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return input, errors.New("redeem: trailing JSON is not allowed")
	}
	return input, nil
}
