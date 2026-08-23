package main

import (
	_ "embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// ferryman は単体の実行ファイルとして配られるので、リンクした MIT / BSD / Apache-2.0 の
// モジュールと、Fyne が go:embed で抱えるフォント（OFL 1.1 ほか）の表示義務を果たせる
// 「添付ドキュメント」が存在しない。だからバイナリ自身が本文を持ち、Menu → Licenses で
// 出す。中身は script/gen-licenses.sh の生成物で、手で書き換えない。
//
//go:generate bash ../../script/gen-licenses.sh
//go:embed NOTICES.txt
var notices string

// showLicenses opens the notices in a window of their own: the text is long and
// wants to be resized and scrolled, which a dialog inside the main window cannot
// do. Only one is kept open at a time; a second request raises the existing one.
func (u *ui) showLicenses() {
	if u.licenseWin != nil {
		u.licenseWin.RequestFocus()
		return
	}

	body := widget.NewLabelWithStyle(notices, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	body.Wrapping = fyne.TextWrapWord

	w := fyne.CurrentApp().NewWindow("Ferryman licenses")
	w.SetContent(container.NewVScroll(body))
	w.Resize(fyne.NewSize(680, 560))
	w.SetOnClosed(func() { u.licenseWin = nil })
	u.licenseWin = w
	w.Show()
}

// mainMenu is the window's menu bar. It exists only to reach the licenses; on
// macOS Fyne merges this into the system menu bar, elsewhere it is drawn as a
// bar above the content.
func (u *ui) mainMenu() *fyne.MainMenu {
	return fyne.NewMainMenu(
		fyne.NewMenu("Menu",
			fyne.NewMenuItem("Licenses", func() { u.showLicenses() }),
		),
	)
}
