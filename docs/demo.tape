# logscry demo recording — vhs tape (https://github.com/charmbracelet/vhs)
#
#   vhs docs/demo.tape        # run from the repo root -> writes docs/demo.gif
#
# Prerequisites (see RECORDING.md for the full walkthrough):
#   - vhs installed
#   - a local Ollama running with gemma2:2b pulled  (ollama pull gemma2:2b)
#   - Docker running
#
# The tape builds logscry, starts the demo compose in the background (hidden), then
# records logscry catching the worker's one fault while the api traffic stays quiet.

Output docs/demo.gif

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 720
Set Padding 20
Set Theme "Catppuccin Mocha"

# --- hidden setup: build + start the demo stream, off-camera ---------------------
Hide
Type "make build" Enter
Sleep 4s
Type "docker compose -f examples/demo/docker-compose.yml up -d" Enter
Sleep 6s
Type "clear" Enter
Show

# --- recorded: point logscry at the stream ---------------------------------------
Type "./bin/logscry --docker-name '^(api|worker)$' --llm-model gemma2:2b" Enter

# Routine traffic scrolls on the left; nothing escalates. The worker's FATAL lands
# ~12s after logscry attaches (FAULT_AFTER=18 less the 6s head start above).
Sleep 13s

# The fault escalates; gemma2:2b explains it and the card appears on the right.
Sleep 6s

# Focus the cards pane and expand the card to show cause + suggestion + context.
Tab
Sleep 1s
Enter
Sleep 5s

# Quit logscry.
Type "q"
Sleep 1s

# --- hidden teardown -------------------------------------------------------------
Hide
Type "docker compose -f examples/demo/docker-compose.yml down" Enter
Sleep 3s
