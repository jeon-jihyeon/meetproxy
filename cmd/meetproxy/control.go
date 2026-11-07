package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/meetproxy/app/control"
)

func runControl(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("control requires call or result")
	}
	flags := flag.NewFlagSet("control", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	binary := flags.String("driver", "cua-driver", "driver executable")
	data := flags.String("data", "", "private operation journal")
	timeout := flags.Duration("timeout", 30*time.Second, "driver timeout")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *data == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*data = filepath.Join(dir, "meetproxy", "operations")
	}
	path, err := exec.LookPath(*binary)
	if err == nil {
		path, err = filepath.Abs(path)
	}
	if err != nil && args[0] != "result" {
		return errors.New("control driver executable was not found")
	}
	client, err := control.Open(control.Config{
		Binary: path, Environment: driverEnvironment(), Timeout: *timeout, Data: *data,
	})
	if err != nil {
		return err
	}
	switch args[0] {
	case "call":
		if flags.NArg() != 0 {
			return errors.New("control call reads its request from JSON stdin")
		}
		return controlCall(ctx, client, in, out)
	case "result":
		if flags.NArg() != 1 {
			return errors.New("control result requires an operation ID")
		}
		result, exists, err := client.Lookup(ctx, flags.Arg(0))
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("control operation was not found")
		}
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	default:
		return errors.New("control requires call or result")
	}
}

func controlCall(ctx context.Context, client *control.Controller, in io.Reader, out io.Writer) error {
	var request control.Request
	body, err := io.ReadAll(io.LimitReader(in, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return errors.New("control request exceeds the input limit or could not be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("control request: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("control request must contain exactly one JSON object")
	}
	result, callErr := client.Execute(ctx, request)
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return errors.Join(encoder.Encode(result), callErr)
}

func driverEnvironment() []string {
	return append(processEnvironment(), "CUA_DRIVER_PERMISSION_MODE=standard", "CUA_DRIVER_RS_TELEMETRY_ENABLED=0")
}

func processEnvironment() []string {
	var env []string
	for _, key := range []string{"HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE",
		"USERPROFILE", "APPDATA", "LOCALAPPDATA", "SYSTEMROOT", "WINDIR", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
