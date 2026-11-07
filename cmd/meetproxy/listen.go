package main

import (
	"context"
	"encoding/xml"
	"errors"
	"flag"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jeon-jihyeon/meetproxy/app/listen"
)

type listenConfig struct {
	directory, token, bot, handler, executable string
	arguments                                  []string
	failures                                   int
	timeout                                    time.Duration
}

func listenFlags(args []string) (listenConfig, error) {
	var config listenConfig
	flags := flag.NewFlagSet("listen", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&config.directory, "data", "", "private listener state directory")
	flags.StringVar(&config.token, "token-file", "", "private Socket Mode app token file")
	flags.StringVar(&config.bot, "bot-user", "", "bot user ID")
	flags.StringVar(&config.handler, "handler", "", "durable receipt executable")
	flags.StringVar(&config.executable, "executable", "", "absolute meetproxy executable for plist output")
	flags.IntVar(&config.failures, "failures", 5, "consecutive connection failure limit")
	flags.DurationVar(&config.timeout, "handler-timeout", 1500*time.Millisecond, "durable receipt deadline")
	flags.Func("handler-arg", "fixed handler argument", func(value string) error {
		config.arguments = append(config.arguments, value)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return config, err
	}
	if flags.NArg() != 0 || config.handler == "" {
		return config, errors.New("listen requires --data, --token-file, --bot-user and --handler")
	}
	binary, err := exec.LookPath(config.handler)
	if err != nil {
		return config, errors.New("listen receipt executable was not found")
	}
	config.handler, err = filepath.Abs(binary)
	return config, err
}

func runListen(ctx context.Context, args []string, out io.Writer) error {
	plist := len(args) > 0 && args[0] == "plist"
	if plist {
		args = args[1:]
	}
	config, err := listenFlags(args)
	if err != nil {
		return err
	}
	source, err := listen.NewSocket(listen.SocketConfig{
		Directory: config.directory, TokenFile: config.token, BotUser: config.bot, Failures: config.failures,
	})
	if err != nil {
		return err
	}
	sink, err := listen.NewCommand(listen.CommandConfig{
		Binary: config.handler, Arguments: config.arguments, Environment: processEnvironment(), Timeout: config.timeout,
	})
	if err != nil {
		return err
	}
	collector, err := listen.New(source, sink)
	if err != nil {
		return err
	}
	if plist {
		return listenPlist(config, out)
	}
	if config.executable != "" {
		return errors.New("--executable is only valid with listen plist")
	}
	return collector.Run(ctx)
}

func listenPlist(config listenConfig, out io.Writer) error {
	if !filepath.IsAbs(config.executable) {
		return errors.New("listen plist requires an absolute --executable")
	}
	arguments := []string{
		config.executable, "listen", "--handler=" + config.handler,
		"--data=" + config.directory, "--token-file=" + config.token, "--bot-user=" + config.bot,
		"--failures=" + strconv.Itoa(config.failures), "--handler-timeout=" + config.timeout.String(),
	}
	for _, argument := range config.arguments {
		arguments = append(arguments, "--handler-arg="+argument)
	}
	if _, err := io.WriteString(out, xml.Header+"<plist version=\"1.0\"><dict><key>Label</key><string>io.meetproxy.listen</string><key>ProgramArguments</key><array>"); err != nil {
		return err
	}
	encoder := xml.NewEncoder(out)
	for _, argument := range arguments {
		if err := encoder.EncodeElement(argument, xml.StartElement{Name: xml.Name{Local: "string"}}); err != nil {
			return err
		}
	}
	if err := encoder.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(out, "</array><key>RunAtLoad</key><true/><key>KeepAlive</key><false/><key>Umask</key><integer>63</integer></dict></plist>\n")
	return err
}
