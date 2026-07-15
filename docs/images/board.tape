# VHS tape to regenerate the home-page board screenshot (docs/images/board.png)
# from a REAL sand board, so the tiles show your own live host/VM stats and
# whatever VMs you actually have.
#
# Requires: charmbracelet/vhs  (brew install vhs), and `sand` on your PATH with
# at least one or two managed VMs so the board isn't empty. Run from this dir:
#
#     vhs board.tape
#
# The committed board.png was generated headlessly from the TUI test fixture
# (charmbracelet/freeze), with a seeded heartbeat so the running VM's cpu/mem
# gauges show representative values; a capture from your own machine shows the
# real ones.

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
Type "sand"
Enter
Sleep 4s
Show
Screenshot board.png
