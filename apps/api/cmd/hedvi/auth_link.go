package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net/mail"
	"net/url"
	"strings"
)

// Invoked only by the explicitly local-only admin auth-mail-link command.
func printLocalAuthLink(raw []byte, appURL string) error {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	body, err := io.ReadAll(quotedprintable.NewReader(m.Body))
	if err != nil {
		return err
	}
	origin, err := url.Parse(appURL)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(body), "\n") {
		u, err := url.Parse(strings.TrimSpace(line))
		if err != nil || u.Scheme != origin.Scheme || u.Host != origin.Host {
			continue
		}
		if u.Path != "/verify-email" && u.Path != "/reset-password" && u.Path != "/forgot-password" {
			continue
		}
		fmt.Println(u.String())
		return nil
	}
	return errors.New("simulated message contained no account link for the configured application origin")
}
