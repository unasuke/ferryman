// Command ferryman is the headless (CLI) front-end. It reads config.json,
// connects, and keeps the enabled forwards up until interrupted.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/unasuke/ferryman/forwarder"
	"golang.org/x/crypto/ssh"
)

func main() {
	cfg := flag.String("config", forwarder.DefaultConfigPath(), "path to config.json")
	flag.Parse()

	profile, err := forwarder.LoadProfile(*cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config:", err)
		os.Exit(1)
	}
	if profile.Host == "" {
		fmt.Fprintf(os.Stderr, "no host set in %s (see README)\n", *cfg)
		os.Exit(1)
	}

	m := forwarder.New(forwarder.NewDialer(profile, passphrasePrompt, trustPrompt))
	m.SetRules(profile.Rules)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go func() {
		for e := range m.Events() {
			printEvent(e)
		}
	}()

	fmt.Printf("connecting to %s ...\n", profile.Host)
	m.Run(ctx)
	fmt.Println("stopped")
}

func printEvent(e forwarder.Event) {
	scope := "connection"
	if e.Rule != "" {
		scope = e.Rule
	}
	if e.Err != nil {
		fmt.Printf("[%s] %s: %v\n", scope, e.State, e.Err)
		return
	}
	fmt.Printf("[%s] %s\n", scope, e.State)
}

func trustPrompt(host string, key ssh.PublicKey) bool {
	fmt.Printf("The authenticity of host %q can't be established.\n", host)
	fmt.Printf("%s key fingerprint is %s\n", key.Type(), ssh.FingerprintSHA256(key))
	fmt.Print("Type yes to continue connecting: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(strings.ToLower(line)) == "yes"
}

func passphrasePrompt(keyPath string) (string, error) {
	// Note: this reads a plain line, so the passphrase is echoed to the
	// terminal. Prefer loading keys into ssh-agent so this prompt is never hit
	// (kept dependency-free and cgo-free on purpose; see the README note).
	fmt.Printf("Enter passphrase for %s: ", keyPath)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
