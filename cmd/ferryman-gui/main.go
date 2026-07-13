// Command ferryman-gui is the single-purpose GUI front-end (Fyne).
// Requires Fyne v2.6+ (for fyne.Do) and a C toolchain to build.
package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/unasuke/ferryman/forwarder"
	"golang.org/x/crypto/ssh"
)

type ui struct {
	win     fyne.Window
	profile forwarder.Profile
	cfgPath string

	mgr    *forwarder.Manager
	cancel context.CancelFunc

	status   *widget.Label
	rulesBox *fyne.Container
	connBtn  *widget.Button
}

func main() {
	a := app.NewWithID("com.unasuke.ferryman")
	w := a.NewWindow("Ferryman")

	cfgPath := forwarder.DefaultConfigPath()
	profile, _ := forwarder.LoadProfile(cfgPath)

	u := &ui{win: w, profile: profile, cfgPath: cfgPath}
	u.status = widget.NewLabel("stopped")
	u.rulesBox = container.NewVBox()
	w.SetContent(u.build())
	u.rebuildRules()
	w.Resize(fyne.NewSize(560, 480))
	w.ShowAndRun()
}

func (u *ui) save() { _ = forwarder.SaveProfile(u.cfgPath, u.profile) }

func (u *ui) build() fyne.CanvasObject {
	host := widget.NewEntry()
	host.SetPlaceHolder("ssh host or alias (from ~/.ssh/config)")
	host.SetText(u.profile.Host)
	host.OnChanged = func(v string) { u.profile.Host = v }

	u.connBtn = widget.NewButton("Connect", func() { u.toggleConnection() })
	top := container.NewBorder(nil, nil, nil, u.connBtn, host)

	name := widget.NewEntry()
	name.SetPlaceHolder("name")
	local := widget.NewEntry()
	local.SetPlaceHolder("8080")
	remote := widget.NewEntry()
	remote.SetPlaceHolder("3000")
	add := widget.NewButton("Add forward", func() {
		// Accept a bare port ("8080") or a full host:port; a bare port gets the
		// default host, so entering an IP is optional.
		localAddr := withDefaultHost(local.Text, "127.0.0.1")
		remoteAddr := withDefaultHost(remote.Text, "localhost")
		if localAddr == "" || remoteAddr == "" {
			return
		}
		r := forwarder.Rule{Name: name.Text, LocalAddr: localAddr, RemoteAddr: remoteAddr, Enabled: true}
		u.profile.Rules = append(u.profile.Rules, r)
		if u.mgr != nil {
			u.mgr.UpsertRule(r)
		}
		u.save()
		u.rebuildRules()
		name.SetText("")
		local.SetText("")
		remote.SetText("")
	})
	// Column headers so the three inputs read as name / local / remote.
	header := container.NewGridWithColumns(3,
		widget.NewLabelWithStyle("Name", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabelWithStyle("Local", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabelWithStyle("Remote", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	)
	form := container.NewVBox(
		widget.NewSeparator(),
		header,
		container.NewGridWithColumns(3, name, local, remote),
		add,
	)

	bottom := container.NewVBox(u.status, form)
	return container.NewBorder(top, bottom, nil, nil, container.NewVScroll(u.rulesBox))
}

func (u *ui) rebuildRules() {
	u.rulesBox.Objects = nil
	for i := range u.profile.Rules {
		i := i
		r := u.profile.Rules[i]

		// Set the initial checked state while OnChanged is still nil, so this
		// rebuild does not fire UpsertRule/save as a side effect. Only wire the
		// handler afterwards, for real user toggles.
		chk := widget.NewCheck("", nil)
		chk.SetChecked(r.Enabled)
		chk.OnChanged = func(on bool) {
			u.profile.Rules[i].Enabled = on
			if u.mgr != nil {
				u.mgr.UpsertRule(u.profile.Rules[i])
			}
			u.save()
		}

		lbl := widget.NewLabel(fmt.Sprintf("%s   local %s  →  remote %s", r.Name, r.LocalAddr, r.RemoteAddr))

		del := widget.NewButton("✕", func() {
			local := u.profile.Rules[i].LocalAddr
			u.profile.Rules = append(u.profile.Rules[:i], u.profile.Rules[i+1:]...)
			if u.mgr != nil {
				u.mgr.DeleteRule(local)
			}
			u.save()
			u.rebuildRules()
		})

		u.rulesBox.Add(container.NewBorder(nil, nil, chk, del, lbl))
	}
	u.rulesBox.Refresh()
}

func (u *ui) toggleConnection() {
	if u.mgr != nil { // disconnect
		u.cancel()
		u.mgr = nil
		u.connBtn.SetText("Connect")
		u.setStatus("stopped")
		return
	}
	u.save()
	m := forwarder.New(forwarder.NewDialer(u.profile, u.passphrase, u.trust))
	m.SetRules(u.profile.Rules)

	ctx, cancel := context.WithCancel(context.Background())
	u.mgr, u.cancel = m, cancel
	u.connBtn.SetText("Disconnect")

	go func() {
		for e := range m.Events() {
			u.onEvent(e)
		}
	}()
	go m.Run(ctx)
}

func (u *ui) onEvent(e forwarder.Event) {
	scope := "connection"
	if e.Rule != "" {
		scope = e.Rule
	}
	msg := fmt.Sprintf("[%s] %s", scope, e.State)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	u.setStatus(msg)
}

// setStatus updates a widget from a background goroutine; fyne.Do marshals the
// call onto Fyne's UI goroutine (requires Fyne v2.6+).
func (u *ui) setStatus(msg string) {
	fyne.Do(func() { u.status.SetText(msg) })
}

func (u *ui) trust(host string, key ssh.PublicKey) bool {
	ch := make(chan bool, 1)
	fyne.Do(func() {
		dialog.ShowConfirm("Unknown host",
			fmt.Sprintf("%s\n%s %s\n\nTrust and continue?", host, key.Type(), ssh.FingerprintSHA256(key)),
			func(ok bool) { ch <- ok }, u.win)
	})
	return <-ch
}

func (u *ui) passphrase(keyPath string) (string, error) {
	// Decide by the dialog's confirm/cancel bool, not by whether the text is
	// empty, so a legitimately empty passphrase is accepted and only Cancel
	// aborts the attempt.
	type result struct {
		text string
		ok   bool
	}
	ch := make(chan result, 1)
	fyne.Do(func() {
		entry := widget.NewPasswordEntry()
		dialog.ShowForm("Key passphrase", "Unlock", "Cancel",
			[]*widget.FormItem{widget.NewFormItem(keyPath, entry)},
			func(ok bool) { ch <- result{text: entry.Text, ok: ok} }, u.win)
	})
	r := <-ch
	if !r.ok {
		return "", fmt.Errorf("cancelled")
	}
	return r.text, nil
}

// withDefaultHost lets the user type just a port. A bare numeric port is
// expanded to defaultHost:port; anything already carrying a host (":8080",
// "127.0.0.1:8080", "[::1]:5173") is returned unchanged so IPv6 and non-loopback
// targets still work. Empty input stays empty.
func withDefaultHost(input, defaultHost string) string {
	s := strings.TrimSpace(input)
	if s == "" {
		return ""
	}
	if _, err := strconv.Atoi(s); err == nil {
		return net.JoinHostPort(defaultHost, s)
	}
	return s
}
