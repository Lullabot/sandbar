# Terminal and tmux

Sandbar ships `~/.tmux.conf` with **Ctrl-A** as the tmux prefix. Press Ctrl-A,
release it, then an arrow key to move between panes. Ctrl-A then `|` splits
side by side; Ctrl-A then capital `S` splits top and bottom. Both new panes
start in the current pane's directory. Ctrl-A then `c` makes a new window;
Ctrl-A then `d` detaches. Mouse support and vi copy mode are enabled. A
detached session continues because systemd linger is enabled for the guest
user. These are shipped defaults; a user may have changed the live server.

Inspect the running tmux server with `tmux show -gv prefix` and
`tmux list-keys -T prefix`. `tmux source-file ~/.tmux.conf` reloads the file;
the shipped binding is Ctrl-A then `r`. If Sandbar was launched from a host
tmux, there may be an outer prefix to pass through before the guest sees
Ctrl-A. Inspect that outer tmux separately.

Text selected in guest tmux can reach the workstation terminal through OSC 52
when the terminal supports it. The guest's image clipboard is a separate,
image-only slot; it does not expose workstation clipboard text to the guest.
