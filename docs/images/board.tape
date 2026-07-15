# VHS tape to regenerate the home-page board screenshot (docs/images/board.png)
# from a REAL sand board, so the tiles show live host/VM stats instead of the
# placeholder dashes in the committed image.
#
# Requires: charmbracelet/vhs  (brew install vhs), and `sand` on your PATH with
# at least one or two managed VMs so the board isn't empty. Run from this dir:
#
#     vhs board.tape
#
# The committed board.png was generated headlessly from the TUI test fixture
# (charmbracelet/freeze on the wide-board golden), which is why its CPU/memory
# bars read "—"; a capture from your own machine will show real values.

Output board.png

Set Shell "bash"
Set FontFamily "JetBrains Mono"
Set FontSize 20
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
