# VHS tape to regenerate the home-page board screenshot (docs/images/board.png)
# from a REAL sandbar board, so the tiles show your own live host/VM stats and
# whatever VMs you actually have.
#
# Requires: charmbracelet/vhs  (brew install vhs), and `sandbar` on your PATH with
# at least one or two managed VMs so the board isn't empty. Run from this dir:
#
#     vhs board.tape
#
# The committed board.png is generated deterministically by a committed harness
# (TestGenerateHomeBoardShot in internal/ui/boardshot_test.go) rendered with
# charmbracelet/freeze — see "Regenerating the home-page screenshot" in
# docs/contributing/development.md. This tape is the LIVE alternative: a capture
# from your own machine shows your real VMs and host stats.

Output board.png

Set Shell "bash"
Set FontFamily "JetBrains Mono"
Set FontSize 20
Set LineHeight 1.6
Set Width 1600
Set Height 820
Set Padding 24
Set Margin 0
Set WindowBar Colorful
Set Theme "Github Dark"

Hide
Type "sandbar"
Enter
Sleep 4s
Show
Screenshot board.png
